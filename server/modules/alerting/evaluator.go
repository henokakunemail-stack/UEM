package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"
)

type Evaluator struct {
	db         *sqlx.DB
	repo       *Repository
	httpClient *http.Client
	// webhooks tracks deliveries in flight, so shutdown can let them finish
	// instead of cutting a notification off mid-request.
	webhooks sync.WaitGroup
	// audit records incidents the server resolved; optional.
	audit IncidentAuditor
}

// WaitWebhooks waits up to timeout for in-flight webhook deliveries.
func (e *Evaluator) WaitWebhooks(timeout time.Duration) {
	done := make(chan struct{})
	go func() { e.webhooks.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Warn().Msg("shutdown: alert webhooks still in flight were abandoned")
	}
}

func isPrivateOrBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// Check CGNAT (100.64.0.0/10) and 0.0.0.0/8
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 {
			return true
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		// 169.254.x.x link local / cloud metadata
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
		// 127.x.x.x
		if ip4[0] == 127 {
			return true
		}
	}
	return false
}

func newSafeWebhookClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("no IP address found for %s", host)
			}
			for _, ip := range ips {
				if isPrivateOrBlockedIP(ip) {
					return nil, fmt.Errorf("webhook destination %s resolves to blocked/private IP: %s", host, ip.String())
				}
			}
			safeAddr := net.JoinHostPort(ips[0].String(), port)
			return dialer.DialContext(ctx, network, safeAddr)
		},
		ResponseHeaderTimeout: 5 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("unsupported redirect scheme: %s", req.URL.Scheme)
			}
			return nil
		},
	}
}

// IncidentAuditor records incidents the server resolved on its own.
type IncidentAuditor interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

// WithAuditor makes system resolutions appear in the audit trail.
func (e *Evaluator) WithAuditor(a IncidentAuditor) *Evaluator {
	e.audit = a
	return e
}

// DefaultOfflineMinutes is the threshold a device_offline rule uses when it
// has none. Migration 0028 wrote it into rules stored with 0.
const DefaultOfflineMinutes = 15

func NewEvaluator(db *sqlx.DB, repo *Repository) *Evaluator {
	return &Evaluator{
		db:         db,
		repo:       repo,
		httpClient: newSafeWebhookClient(),
	}
}

type EvalResult struct {
	RulesEvaluated    int `json:"rules_evaluated"`
	IncidentsNew      int `json:"incidents_new"`
	IncidentsDedup    int `json:"incidents_dedup"`
	IncidentsResolved int `json:"incidents_resolved"`
}

func (e *Evaluator) EvaluateAll(ctx context.Context) (*EvalResult, error) {
	rules, err := e.repo.ListRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list rules for eval: %w", err)
	}

	result := &EvalResult{RulesEvaluated: 0, IncidentsNew: 0, IncidentsDedup: 0}

	for _, rule := range rules {
		if !rule.IsEnabled {
			continue
		}
		result.RulesEvaluated++

		var firing map[string]bool
		var ok bool
		switch rule.RuleType {
		case "disk_low":
			firing, ok = e.evalDiskLow(ctx, &rule, result)
		case "device_offline":
			firing, ok = e.evalDeviceOffline(ctx, &rule, result)
		case "critical_patch":
			firing, ok = e.evalCriticalPatches(ctx, &rule, result)
		default:
			continue
		}
		// Only a query that answered can say a condition cleared. A failed
		// one says nothing, and closing incidents on it would report every
		// device healthy during a database hiccup.
		if ok {
			result.IncidentsResolved += e.resolveCleared(ctx, &rule, firing, "condition_cleared")
		}
	}

	return result, nil
}

// resolveCleared closes the rule's active incidents whose device is no
// longer firing. Incidents never closed on their own before: a disk that was
// cleaned up, or a device that came back, left its incident open for good.
func (e *Evaluator) resolveCleared(ctx context.Context, rule *AlertRule, firing map[string]bool, reason string) int {
	active, err := e.repo.ActiveIncidentsForRule(ctx, rule.ID)
	if err != nil {
		log.Error().Err(err).Str("rule", rule.ID).Msg("list active incidents")
		return 0
	}
	resolved := 0
	for _, inc := range active {
		if firing[inc.DeviceID] {
			continue
		}
		if e.ResolveAsSystem(ctx, rule, inc.ID, reason) {
			resolved++
		}
	}
	return resolved
}

// ResolveAsSystem closes one incident as the server, records why, and tells
// the rule's webhook. It reports whether the incident was still open.
func (e *Evaluator) ResolveAsSystem(ctx context.Context, rule *AlertRule, incidentID, reason string) bool {
	changed, err := e.repo.ResolveBySystem(ctx, incidentID)
	if err != nil {
		log.Error().Err(err).Str("incident", incidentID).Msg("auto-resolve incident")
		return false
	}
	if !changed {
		return false
	}
	if e.audit != nil {
		_ = e.audit.Log(ctx, "system", SystemActor, "alert.incident_auto_resolved", incidentID, map[string]string{
			"rule_id": rule.ID, "reason": reason,
		})
	}
	if inc, err := e.repo.GetIncidentByID(ctx, incidentID); err == nil {
		e.DispatchResolved(rule, inc, reason)
	}
	return true
}

// ResolveRule closes every active incident of a rule, as when it is disabled.
func (e *Evaluator) ResolveRule(ctx context.Context, rule *AlertRule, reason string) int {
	return e.resolveCleared(ctx, rule, nil, reason)
}

type diskTarget struct {
	DeviceID    string   `db:"device_id"`
	Hostname    string   `db:"hostname"`
	Site        string   `db:"site"`
	DiskFreePct *float64 `db:"hw_disk_free_pct"`
}

func (e *Evaluator) evalDiskLow(ctx context.Context, rule *AlertRule, res *EvalResult) (map[string]bool, bool) {
	var targets []diskTarget
	query := `
		SELECT d.id as device_id, d.hostname, COALESCE(d.site, '') AS site, i.hw_disk_free_pct
		FROM devices d
		JOIN device_inventory i ON i.device_id = d.id
		WHERE d.retired_at IS NULL AND i.hw_disk_free_pct IS NOT NULL AND i.hw_disk_free_pct < ?
	`
	if err := e.db.SelectContext(ctx, &targets, query, rule.ThresholdVal); err != nil {
		log.Error().Err(err).Str("rule", rule.ID).Msg("eval disk_low query")
		return nil, false
	}

	firing := map[string]bool{}
	for _, t := range targets {
		firing[t.DeviceID] = true
		free := 0.0
		if t.DiskFreePct != nil {
			free = *t.DiskFreePct
		}
		title := fmt.Sprintf("Low Disk Space: %s (%.1f%% free)", t.Hostname, free)
		msg := fmt.Sprintf("Device %s at site '%s' has only %.1f%% disk space remaining (threshold: %.1f%%)",
			t.Hostname, t.Site, free, rule.ThresholdVal)

		inc, isNew, err := e.repo.UpsertIncident(ctx, rule.ID, t.DeviceID, rule.Severity, title, msg, free)
		if err != nil {
			log.Error().Err(err).Str("device", t.DeviceID).Msg("upsert disk_low incident")
			continue
		}

		if isNew {
			res.IncidentsNew++
			e.dispatchWebhook(rule, inc)
		} else {
			res.IncidentsDedup++
		}
	}
	return firing, true
}

type offlineTarget struct {
	DeviceID string `db:"id"`
	Hostname string `db:"hostname"`
	Site     string `db:"site"`
	// status = 'offline' is the DEFAULT for a device that has an enrollment
	// token but has never checked in, so last_seen_at is legitimately NULL for
	// every one of those. This is a pointer rather than a COALESCE because
	// "never seen" is a different fact from "seen at the zero time", and the
	// message below renders them differently.
	LastSeenAt *time.Time `db:"last_seen_at"`
}

func (e *Evaluator) evalDeviceOffline(ctx context.Context, rule *AlertRule, res *EvalResult) (map[string]bool, bool) {
	// Offline for longer than the rule's threshold, in minutes. The threshold
	// used to be ignored, so a laptop that slept through one heartbeat fired
	// at once. A device never seen at all is a pending enrollment, not an
	// outage, and is left out: it used to fire as "Last seen at never".
	minutes := rule.ThresholdVal
	if minutes <= 0 {
		minutes = DefaultOfflineMinutes
	}
	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(minutes * float64(time.Minute)))

	var targets []offlineTarget
	query := `
		SELECT id, hostname, COALESCE(site, '') AS site, last_seen_at
		FROM devices
		WHERE retired_at IS NULL AND status = 'offline'
		  AND last_seen_at IS NOT NULL AND last_seen_at < ?
	`
	if err := e.db.SelectContext(ctx, &targets, query, cutoff); err != nil {
		log.Error().Err(err).Str("rule", rule.ID).Msg("eval device_offline query")
		return nil, false
	}

	firing := map[string]bool{}
	for _, t := range targets {
		firing[t.DeviceID] = true
		offlineMins := 0.0
		lastSeen := "never"
		if t.LastSeenAt != nil {
			lastSeen = t.LastSeenAt.Format(time.RFC3339)
			offlineMins = now.Sub(*t.LastSeenAt).Minutes()
		}
		title := fmt.Sprintf("Device Offline: %s", t.Hostname)
		msg := fmt.Sprintf("Device %s at site '%s' is offline. Last seen at %s",
			t.Hostname, t.Site, lastSeen)

		inc, isNew, err := e.repo.UpsertIncident(ctx, rule.ID, t.DeviceID, rule.Severity, title, msg, offlineMins)
		if err != nil {
			log.Error().Err(err).Str("device", t.DeviceID).Msg("upsert offline incident")
			continue
		}

		if isNew {
			res.IncidentsNew++
			e.dispatchWebhook(rule, inc)
		} else {
			res.IncidentsDedup++
		}
	}
	return firing, true
}

type patchTarget struct {
	DeviceID   string `db:"device_id"`
	Hostname   string `db:"hostname"`
	Site       string `db:"site"`
	PatchCount int    `db:"patch_count"`
}

func (e *Evaluator) evalCriticalPatches(ctx context.Context, rule *AlertRule, res *EvalResult) (map[string]bool, bool) {
	var targets []patchTarget
	query := `
		SELECT p.device_id, d.hostname, COALESCE(d.site, '') AS site, COUNT(*) as patch_count
		FROM device_patches p
		JOIN devices d ON d.id = p.device_id
		WHERE d.retired_at IS NULL AND p.severity = 'critical' AND p.installed_state = 'missing'
		GROUP BY p.device_id, d.hostname, d.site
		HAVING COUNT(*) >= ?
	`
	threshold := int(rule.ThresholdVal)
	if threshold <= 0 {
		threshold = 1
	}

	if err := e.db.SelectContext(ctx, &targets, query, threshold); err != nil {
		log.Error().Err(err).Str("rule", rule.ID).Msg("eval critical_patch query")
		return nil, false
	}

	firing := map[string]bool{}
	for _, t := range targets {
		firing[t.DeviceID] = true
		title := fmt.Sprintf("Critical Patches Missing: %s (%d patches)", t.Hostname, t.PatchCount)
		msg := fmt.Sprintf("Device %s at site '%s' has %d uninstalled critical security patches",
			t.Hostname, t.Site, t.PatchCount)

		inc, isNew, err := e.repo.UpsertIncident(ctx, rule.ID, t.DeviceID, rule.Severity, title, msg, float64(t.PatchCount))
		if err != nil {
			log.Error().Err(err).Str("device", t.DeviceID).Msg("upsert patch incident")
			continue
		}

		if isNew {
			res.IncidentsNew++
			e.dispatchWebhook(rule, inc)
		} else {
			res.IncidentsDedup++
		}
	}
	return firing, true
}

func (e *Evaluator) dispatchWebhook(rule *AlertRule, inc *AlertIncident) {
	e.sendWebhook(rule, inc, "alert.incident", "")
}

// DispatchResolved tells the rule's webhook an incident was resolved, by an
// operator or by the server. Receivers only ever heard about incidents
// opening, so a pager stayed red after the problem was gone.
func (e *Evaluator) DispatchResolved(rule *AlertRule, inc *AlertIncident, reason string) {
	e.sendWebhook(rule, inc, "alert.resolved", reason)
}

func (e *Evaluator) sendWebhook(rule *AlertRule, inc *AlertIncident, event, reason string) {
	if rule.WebhookURL == "" || inc == nil {
		return
	}

	u, err := url.Parse(rule.WebhookURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		log.Warn().Str("url", rule.WebhookURL).Msg("dispatch alert webhook rejected: invalid scheme")
		return
	}

	payload := map[string]any{
		"event":         event,
		"incident_id":   inc.ID,
		"rule_id":       rule.ID,
		"rule_name":     rule.Name,
		"severity":      inc.Severity,
		"title":         inc.Title,
		"message":       inc.Message,
		"device_id":     inc.DeviceID,
		"hostname":      inc.Hostname,
		"site":          inc.Site,
		"status":        inc.Status,
		"trigger_count": inc.TriggerCount,
		"timestamp":     inc.LastTriggeredAt.Format(time.RFC3339),
	}
	if reason != "" {
		payload["reason"] = reason
	}

	e.webhooks.Add(1)
	go func(url string, data map[string]any) {
		defer e.webhooks.Done()
		bodyBytes, err := json.Marshal(data)
		if err != nil {
			return
		}
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "EndpointMgmt-Alerting/1.0")

		resp, err := e.httpClient.Do(req)
		if err != nil {
			log.Warn().Err(err).Str("url", url).Msg("dispatch alert webhook failed")
			return
		}
		_ = resp.Body.Close()
		log.Debug().Str("url", url).Int("status", resp.StatusCode).Msg("dispatch alert webhook ok")
	}(rule.WebhookURL, payload)
}

// StartBackgroundEvaluator runs EvaluateAll on a ticker until ctx is cancelled.
//
// The context is not decoration. This used to be `for range ticker.C` with no
// way out and no ticker.Stop(), so it could not be shut down at shutdown and a
// second evaluator could be started beside a running one -- every rule then
// evaluated twice per tick, and every incident written twice. main.go's cleanup
// closure could not stop it because there was nothing to call.
func (e *Evaluator) StartBackgroundEvaluator(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		// Inside the goroutine, not in a defer here. This function returns as
		// soon as the goroutine is launched, so a defer on this line stopped the
		// ticker immediately -- and a stopped ticker never fires again, so the
		// evaluator silently evaluated nothing for the life of the process,
		// while still looking alive and correctly stoppable.
		defer ticker.Stop()
		// Once at start, so a restart does not leave alerts 30 seconds stale.
		e.runOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.runOnce(ctx)
			}
		}
	}()
}

func (e *Evaluator) runOnce(ctx context.Context) {
	evalCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := e.EvaluateAll(evalCtx); err != nil && ctx.Err() == nil {
		log.Warn().Err(err).Msg("alert evaluation")
	}
}
