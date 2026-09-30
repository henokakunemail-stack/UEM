//go:build windows

package patch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const winScanScript = `
$ErrorActionPreference = 'Stop'
$payload = $null
try {
    $session = New-Object -ComObject Microsoft.Update.Session
    $searcher = $session.CreateUpdateSearcher()
    $searcher.ServerSelection = 2
    $result = $searcher.Search("IsInstalled=0 and Type='Software'")
    $list = @()
    if ($result -and $result.Updates) {
        foreach ($u in $result.Updates) {
            $kb = ""
            if ($u.KBArticleIDs -and $u.KBArticleIDs.Count -gt 0) {
                $kb = "KB" + $u.KBArticleIDs[0]
            }
            $sev = "unspecified"
            switch ($u.MsrcSeverity) {
                "Critical"  { $sev = "critical" }
                "Important" { $sev = "important" }
                "Moderate"  { $sev = "moderate" }
                "Low"        { $sev = "low" }
            }
            $cat = "security"
            if ($u.Categories -and $u.Categories.Count -gt 0) {
                $catName = $u.Categories[0].Name
                if ($catName -match "Security") { $cat = "security" }
                elseif ($catName -match "Critical") { $cat = "critical" }
                elseif ($catName -match "Definition") { $cat = "definition" }
                else { $cat = "updates" }
            }
            $patchIdent = if ($kb -ne "") { $kb } else { $u.Identity.UpdateID }
            $list += [PSCustomObject]@{
                patch_id        = [string]$patchIdent
                title           = [string]$u.Title
                description     = [string]$u.Description
                severity        = [string]$sev
                category        = [string]$cat
                kb_id           = [string]$kb
                size_bytes      = 0
                installed_state = "missing"
                reboot_required = [bool]$u.RebootRequired
            }
        }
    }
    $payload = [PSCustomObject]@{ ok = $true; error = ""; patches = @($list) }
} catch {
    $payload = [PSCustomObject]@{ ok = $false; error = [string]$_.Exception.Message; patches = @() }
}
# -InputObject keeps a single-element array from collapsing into a bare object.
ConvertTo-Json -InputObject $payload -Compress
`

func scanOS(ctx context.Context) ([]PatchItem, error) {
	return runScanScript(ctx, winScanScript)
}

// scanEnvelope is the result document the scan script emits on both the success
// and the failure path, so a broken scan is never indistinguishable from an
// up-to-date device.
type scanEnvelope struct {
	OK      bool        `json:"ok"`
	Error   string      `json:"error"`
	Patches []PatchItem `json:"patches"`
}

func runScanScript(ctx context.Context, script string) ([]PatchItem, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, "powershell.exe",
		"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("powershell update scan: %w", err)
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, fmt.Errorf("windows update scan: script produced no output")
	}

	// Tolerate the pipeline form of ConvertTo-Json, which collapses a
	// single-element array into a bare object; -InputObject on the script side
	// avoids this, but a hand-run or older agent may still emit it.
	if strings.HasPrefix(trimmed, "[") {
		var items []PatchItem
		if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
			return nil, fmt.Errorf("unmarshal patches list: %w", err)
		}
		return items, nil
	}

	var env scanEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return nil, fmt.Errorf("unmarshal scan envelope: %w", err)
	}
	if !env.OK {
		reason := env.Error
		if reason == "" {
			reason = "no reason reported"
		}
		return nil, fmt.Errorf("windows update scan failed: %s", reason)
	}
	if env.Patches == nil {
		return []PatchItem{}, nil
	}
	return env.Patches, nil
}
