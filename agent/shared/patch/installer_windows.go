//go:build windows

package patch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func installOS(ctx context.Context, params InstallParams) (InstallResult, error) {
	if len(params.PatchIDs) == 0 {
		return InstallResult{
			JobID:        params.JobID,
			Status:       "failed",
			ErrorMessage: "no patches specified for installation",
		}, nil
	}
	return installOSWithPrefix(ctx, "", params)
}

// buildInstallScript builds the PowerShell install script. The join separator
// uses PowerShell newline char [char]10.
func buildInstallScript(idArrayStr string) string {
	return fmt.Sprintf("$ErrorActionPreference = 'Stop'\n"+
		"$targetIDs = @(%s)\n"+
		"$output = @()\n"+
		"$rebootNeeded = $false\n"+
		"$allSuccess = $true\n"+
		"$payload = $null\n"+
		"try {\n"+
		"  $session = New-Object -ComObject Microsoft.Update.Session\n"+
		"  $searcher = $session.CreateUpdateSearcher()\n"+
		"  $searcher.ServerSelection = 2\n"+
		"  $searchResult = $searcher.Search(\"IsInstalled=0 and Type='Software'\")\n"+
		"  $toDownload = New-Object -ComObject Microsoft.Update.UpdateColl\n"+
		"  foreach ($u in $searchResult.Updates) {\n"+
		"    $kb = ''\n"+
		"    if ($u.KBArticleIDs -and $u.KBArticleIDs.Count -gt 0) {\n"+
		"      $kb = 'KB' + $u.KBArticleIDs[0]\n"+
		"    }\n"+
		"    $patchIdent = if ($kb -ne '') { $kb } else { $u.Identity.UpdateID }\n"+
		"    if ($targetIDs -contains $patchIdent) {\n"+
		"      $toDownload.Add($u) | Out-Null\n"+
		"      $output += \"Matched patch: $patchIdent\"\n"+
		"    }\n"+
		"  }\n"+
		"  if ($toDownload.Count -gt 0) {\n"+
		"    $output += \"Downloading $($toDownload.Count) updates...\"\n"+
		"    $downloader = $session.CreateUpdateDownloader()\n"+
		"    $downloader.Updates = $toDownload\n"+
		"    $downloader.Download() | Out-Null\n"+
		"    $output += 'Installing updates...'\n"+
		"    $installer = $session.CreateUpdateInstaller()\n"+
		"    $installer.Updates = $toDownload\n"+
		"    $installResult = $installer.Install()\n"+
		"    $rebootNeeded = [bool]$installResult.RebootRequired\n"+
		"    $output += \"Installation completed with resultCode: $($installResult.ResultCode)\"\n"+
		"    if ($installResult.ResultCode -ne 2) { $allSuccess = $false }\n"+
		"  } else {\n"+
		"    $allSuccess = $false\n"+
		"    $output += 'No available update matched the requested targets: ' + ($targetIDs -join ', ')\n"+
		"  }\n"+
		"} catch {\n"+
		"  $allSuccess = $false\n"+
		"  $output += 'WUA Execution notice: ' + $_.Exception.Message\n"+
		"}\n"+
		"$status = if ($allSuccess) { 'completed' } else { 'failed' }\n"+
		"$errorMessage = if ($allSuccess) { '' } else { [string]($output -join [char]10) }\n"+
		"ConvertTo-Json -InputObject ([PSCustomObject]@{ status = $status; reboot = $rebootNeeded; log = ($output -join [char]10); error = $errorMessage }) -Compress\n",
		idArrayStr)
}

// installOSWithPrefix runs the install script with an optional preamble, which
// tests use to shadow New-Object with a fake Windows Update session. Production
// passes an empty prefix.
func installOSWithPrefix(ctx context.Context, prefix string, params InstallParams) (InstallResult, error) {
	result := InstallResult{
		JobID:  params.JobID,
		Status: "failed",
	}

	if len(params.PatchIDs) == 0 {
		result.ErrorMessage = "no patches specified for installation"
		return result, nil
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	// Convert patch IDs to comma-separated PowerShell array
	var quotedIDs []string
	for _, id := range params.PatchIDs {
		quotedIDs = append(quotedIDs, fmt.Sprintf("'%s'", strings.ReplaceAll(id, "'", "''")))
	}
	idArrayStr := strings.Join(quotedIDs, ",")

	script := prefix + buildInstallScript(idArrayStr)

	cmd := exec.CommandContext(ctxTimeout, "powershell.exe",
		"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil && stdout.Len() == 0 {
		result.Status = "failed"
		result.ErrorMessage = fmt.Sprintf("execution error: %v, stderr: %s", err, stderr.String())
		result.OutputLog = stdout.String()
		return result, nil
	}

	outStr := strings.TrimSpace(stdout.String())
	result.OutputLog = outStr

	var env struct {
		Status       string `json:"status"`
		Reboot       bool   `json:"reboot"`
		Log          string `json:"log"`
		ErrorMessage string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(outStr), &env); jerr != nil {
		// Unparseable output is a failure, never a silent success.
		result.Status = "failed"
		result.ErrorMessage = fmt.Sprintf("unparseable install output: %v", jerr)
		return result, nil
	}

	result.Status = env.Status
	result.RebootRequired = env.Reboot
	if env.Log != "" {
		result.OutputLog = env.Log
	}
	if env.Status != "completed" {
		result.ErrorMessage = env.ErrorMessage
		if result.ErrorMessage == "" {
			result.ErrorMessage = "installation did not complete"
		}
	}

	return result, nil
}
