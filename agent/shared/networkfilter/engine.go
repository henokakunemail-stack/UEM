package networkfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/transport"
)

const (
	MarkerBegin = "### BEGIN ENDPOINT-MGMT MANAGED BLOCKLIST ###"
	MarkerEnd   = "### END ENDPOINT-MGMT MANAGED BLOCKLIST ###"
)

type Engine struct {
	hostsPath    string
	serverURL    string
	deviceID     string
	deviceSecret string
	httpClient   *http.Client
	mu           sync.Mutex
	resolver     *resolver
	firewall     firewallBackend
}

func NewEngine(serverURL, deviceID, deviceSecret string) *Engine {
	hosts := defaultHostsPath()
	if envPath := os.Getenv("ENDPOINT_MGMT_HOSTS_PATH"); envPath != "" {
		hosts = envPath
	}

	return &Engine{
		hostsPath:    hosts,
		serverURL:    serverURL,
		deviceID:     deviceID,
		deviceSecret: deviceSecret,
		httpClient:   transport.NewHTTPClient(10 * time.Second),
		resolver:     newResolver(),
		firewall:     newFirewallBackend(),
	}
}

func defaultHostsPath() string {
	if runtime.GOOS == "windows" {
		winDir := os.Getenv("SystemRoot")
		if winDir == "" {
			winDir = `C:\Windows`
		}
		return filepath.Join(winDir, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

func (e *Engine) SetHostsPath(p string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hostsPath = p
}

// ApplyBlockedDomains enforces the block list on both layers: the hosts file gets
// one sinkhole line per domain, and the firewall gets every address those domains
// resolve to.
//
// The two layers exist because neither covers the other. A hosts file cannot block
// a subdomain, so www.detik.com survives a rule naming detik.com. A firewall
// cannot be applied without elevation, so on a non-elevated agent it is the only
// thing that would work -- and it cannot be relied on there.
//
// The hosts layer runs first and its failure is an error, because it is the layer
// that must work. A firewall failure is not fatal, but it is returned as `degraded`
// rather than swallowed: a silent fallback is how a device ends up reporting
// "synced" while only the weaker layer is enforced, which is the state an operator
// must be able to see. An empty degraded string means both layers applied.
func (e *Engine) ApplyBlockedDomains(domains []string) (count int, degraded string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	hostsCount, err := e.writeHosts(domains)
	if err != nil {
		return 0, "", err
	}

	blocked, resolveErrs, fwErr := applyFirewall(e.firewall, e.resolver, domains)
	var warnings []string
	for domain, msg := range resolveErrs {
		log.Warn().Str("domain", domain).Str("error", msg).
			Msg("filter rule could not be resolved; its addresses are not blocked")
		warnings = append(warnings, fmt.Sprintf("%s: %s", domain, msg))
	}
	if fwErr != nil {
		msg := describeFirewallError(fwErr)
		log.Warn().Str("error", msg).Int("hosts_rules", hostsCount).
			Msg("firewall layer unavailable; enforcement is limited to the hosts file")
		warnings = append(warnings, msg)
	}
	// Sorted so the same policy produces the same report text, which is what makes
	// a changed message meaningful to an operator reading the device's history.
	sort.Strings(warnings)

	if len(warnings) > 0 {
		return hostsCount, strings.Join(warnings, "; "), nil
	}
	log.Info().Int("hosts_rules", hostsCount).Int("addresses_blocked", blocked).
		Str("path", e.hostsPath).Msg("network filter rules applied")
	return hostsCount, "", nil
}

// writeHosts rewrites the managed section of the hosts file. It is the layer that
// must always succeed, so every failure here is returned rather than tolerated.
func (e *Engine) writeHosts(domains []string) (int, error) {
	var existingContent []byte
	if _, err := os.Stat(e.hostsPath); err == nil {
		data, err := os.ReadFile(e.hostsPath)
		if err != nil {
			return 0, fmt.Errorf("read hosts file: %w", err)
		}
		existingContent = data
	}

	// Clean out previous managed section if present
	cleanContent := removeManagedSection(string(existingContent))

	// Build new managed section
	var sb strings.Builder
	sb.WriteString(cleanContent)
	if !strings.HasSuffix(cleanContent, "\n") && len(cleanContent) > 0 {
		sb.WriteString("\n")
	}

	appliedCount := 0
	if len(domains) > 0 {
		sb.WriteString(MarkerBegin + "\n")
		sb.WriteString("# Managed by Endpoint Management Platform - DO NOT EDIT MANUALLY\n")
		seen := make(map[string]bool)
		for _, d := range domains {
			domain := normalizeDomain(d)
			if domain == "" || strings.HasPrefix(domain, "#") {
				continue
			}
			appliedCount++

			var variants []string
			if strings.HasPrefix(domain, "www.") {
				root := strings.TrimPrefix(domain, "www.")
				variants = []string{domain, root}
			} else {
				variants = []string{domain, "www." + domain}
			}

			for _, v := range variants {
				if seen[v] {
					continue
				}
				seen[v] = true
				fmt.Fprintf(&sb, "0.0.0.0 %s\n", v)
				fmt.Fprintf(&sb, ":: %s\n", v)
			}
		}
		sb.WriteString(MarkerEnd + "\n")
	}

	// Write atomically or write directly
	if err := os.WriteFile(e.hostsPath, []byte(sb.String()), 0644); err != nil {
		return 0, fmt.Errorf("write hosts file: %w", err)
	}

	// Flush OS DNS resolver cache
	e.flushDNS()
	return appliedCount, nil
}

func removeManagedSection(content string) string {
	beginIdx := strings.Index(content, MarkerBegin)
	if beginIdx == -1 {
		return content
	}

	endIdx := strings.Index(content, MarkerEnd)
	if endIdx == -1 {
		// Incomplete marker: strip from beginIdx
		return strings.TrimRight(content[:beginIdx], "\r\n ") + "\n"
	}

	afterEnd := endIdx + len(MarkerEnd)
	// Skip newline after MarkerEnd if present
	if afterEnd < len(content) && (content[afterEnd] == '\n' || content[afterEnd] == '\r') {
		afterEnd++
		if afterEnd < len(content) && content[afterEnd] == '\n' {
			afterEnd++
		}
	}

	cleaned := content[:beginIdx] + content[afterEnd:]
	return strings.TrimRight(cleaned, "\r\n ") + "\n"
}

func (e *Engine) flushDNS() {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("ipconfig", "/flushdns").Run()
	case "darwin":
		_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()
	case "linux":
		_ = exec.Command("resolvectl", "flush-caches").Run()
	}
}

// ReportFilterState reports policy enforcement status to server
func (e *Engine) ReportFilterState(ctx context.Context, version, status string, count int, errMsg string) error {
	reportURL := fmt.Sprintf("%s/api/agent/devices/%s/filter/report", strings.TrimRight(e.serverURL, "/"), e.deviceID)

	payload := map[string]any{
		"policy_version": version,
		"status":         status,
		"rules_applied":  count,
		"error_message":  errMsg,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal filter report: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reportURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create filter report request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Id", e.deviceID)
	req.Header.Set("X-Device-Secret", e.deviceSecret)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send filter report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("filter report HTTP %d", resp.StatusCode)
	}
	return nil
}
