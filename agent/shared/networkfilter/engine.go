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

// ApplyBlockedDomains updates the system hosts file with 0.0.0.0 sinkholes inside managed markers
func (e *Engine) ApplyBlockedDomains(domains []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

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
		for _, d := range domains {
			domain := strings.TrimSpace(d)
			if domain == "" || strings.HasPrefix(domain, "#") {
				continue
			}
			// Clean domain from protocols or wildcards
			domain = strings.TrimPrefix(domain, "http://")
			domain = strings.TrimPrefix(domain, "https://")
			domain = strings.TrimPrefix(domain, "*.")
			parts := strings.Split(domain, "/")
			cleanDomain := parts[0]

			fmt.Fprintf(&sb, "0.0.0.0 %s\n", cleanDomain)
			appliedCount++
		}
		sb.WriteString(MarkerEnd + "\n")
	}

	// Write atomically or write directly
	if err := os.WriteFile(e.hostsPath, []byte(sb.String()), 0644); err != nil {
		return 0, fmt.Errorf("write hosts file: %w", err)
	}

	// Flush OS DNS resolver cache
	e.flushDNS()

	log.Info().Int("rules_applied", appliedCount).Str("path", e.hostsPath).Msg("network filter rules applied successfully")
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
