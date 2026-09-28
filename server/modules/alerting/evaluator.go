package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"
)

type Evaluator struct {
	db         *sqlx.DB
	repo       *Repository
	httpClient *http.Client
}

func NewEvaluator(db *sqlx.DB, repo *Repository) *Evaluator {
	return &Evaluator{
		db:   db,
		repo: repo,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

type EvalResult struct {
	RulesEvaluated int `json:"rules_evaluated"`
	IncidentsNew   int `json:"incidents_new"`
	IncidentsDedup int `json:"incidents_dedup"`
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

		switch rule.RuleType {
		case "disk_low":
			e.evalDiskLow(ctx, &rule, result)
		case "device_offline":
			e.evalDeviceOffline(ctx, &rule, result)
		case "critical_patch":
			e.evalCriticalPatches(ctx, &rule, result)
		}
	}

	return result, nil
}

type diskTarget struct {
	DeviceID    string   `db:"device_id"`
	Hostname    string   `db:"hostname"`
	Site        string   `db:"site"`
	DiskFreePct *float64 `db:"hw_disk_free_pct"`
}

func (e *Evaluator) evalDiskLow(ctx context.Context, rule *AlertRule, res *EvalResult) {
	var targets []diskTarget
	query := `
		SELECT d.id as device_id, d.hostname, COALESCE(d.site, '') AS site, i.hw_disk_free_pct
		FROM devices d
		JOIN device_inventory i ON i.device_id = d.id
		WHERE d.retired_at IS NULL AND i.hw_disk_free_pct IS NOT NULL AND i.hw_disk_free_pct < ?
	`
	if err := e.db.SelectContext(ctx, &targets, query, rule.ThresholdVal); err != nil {
		log.Error().Err(err).Str("rule", rule.ID).Msg("eval disk_low query")
		return
	}

	for _, t := range targets {
		free := 0.0
		if t.DiskFreePct != nil {
			free = *t.DiskFreePct
		}
		title := fmt.Sprintf("Low Disk Space: %s (%.1f%% free)", t.Hostname, free)
		msg := fmt.Sprintf("Device %s at site '%s' has only %.1f%% disk space remaining (threshold: %.1f%%)",
			t.Hostname, t.Site, free, rule.ThresholdVal)

		inc, isNew, err := e.repo.UpsertIncident(ctx, rule.ID, t.DeviceID, rule.Severity, title, msg)
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
}

type offlineTarget struct {
	DeviceID string  `db:"id"`
	Hostname string  `db:"hostname"`
	Site     string  `db:"site"`
	// status = 'offline' is the DEFAULT for a device that has an enrollment
	// token but has never checked in, so last_seen_at is legitimately NULL for
	// every one of those. This is a pointer rather than a COALESCE because
	// "never seen" is a different fact from "seen at the zero time", and the
	// message below renders them differently.
	LastSeenAt *time.Time `db:"last_seen_at"`
}

func (e *Evaluator) evalDeviceOffline(ctx context.Context, rule *AlertRule, res *EvalResult) {
	var targets []offlineTarget
	query := `
		SELECT id, hostname, COALESCE(site, '') AS site, last_seen_at
		FROM devices
		WHERE retired_at IS NULL AND status = 'offline'
	`
	if err := e.db.SelectContext(ctx, &targets, query); err != nil {
		log.Error().Err(err).Str("rule", rule.ID).Msg("eval device_offline query")
		return
	}

	for _, t := range targets {
		lastSeen := "never"
		if t.LastSeenAt != nil {
			lastSeen = t.LastSeenAt.Format(time.RFC3339)
		}
		title := fmt.Sprintf("Device Offline: %s", t.Hostname)
		msg := fmt.Sprintf("Device %s at site '%s' is offline. Last seen at %s",
			t.Hostname, t.Site, lastSeen)

		inc, isNew, err := e.repo.UpsertIncident(ctx, rule.ID, t.DeviceID, rule.Severity, title, msg)
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
}

type patchTarget struct {
	DeviceID   string `db:"device_id"`
	Hostname   string `db:"hostname"`
	Site       string `db:"site"`
	PatchCount int    `db:"patch_count"`
}

func (e *Evaluator) evalCriticalPatches(ctx context.Context, rule *AlertRule, res *EvalResult) {
	var targets []patchTarget
	query := `
		SELECT p.device_id, d.hostname, d.site, COUNT(*) as patch_count
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
		return
	}

	for _, t := range targets {
		title := fmt.Sprintf("Critical Patches Missing: %s (%d patches)", t.Hostname, t.PatchCount)
		msg := fmt.Sprintf("Device %s at site '%s' has %d uninstalled critical security patches",
			t.Hostname, t.Site, t.PatchCount)

		inc, isNew, err := e.repo.UpsertIncident(ctx, rule.ID, t.DeviceID, rule.Severity, title, msg)
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
}

func (e *Evaluator) dispatchWebhook(rule *AlertRule, inc *AlertIncident) {
	if rule.WebhookURL == "" || inc == nil {
		return
	}

	payload := map[string]any{
		"event":         "alert.incident",
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

	go func(url string, data map[string]any) {
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

// StartBackgroundEvaluator starts a periodic evaluation loop.
func (e *Evaluator) StartBackgroundEvaluator(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = e.EvaluateAll(ctx)
			cancel()
		}
	}()
}
