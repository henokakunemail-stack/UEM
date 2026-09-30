//go:build windows

package patch

import (
	"context"
	"strings"
	"testing"
)

// installShim shadows New-Object with a function so installOS talks to a fake
// Windows Update session. Methods must be real ScriptMethods: a NoteProperty
// holding a scriptblock fails with "does not contain a method named".
func installShim() string {
	u1 := `[PSCustomObject]@{
        KBArticleIDs   = @('5007651')
        MsrcSeverity   = 'Critical'
        Categories     = @([PSCustomObject]@{ Name = 'Security Updates' })
        Title          = 'Cumulative Update for Windows'
        Description    = 'A security update.'
        RebootRequired = $false
        Identity       = [PSCustomObject]@{ UpdateID = 'GUID-A' }
    }`
	return `$u1 = ` + u1 + `
$searchResult = [PSCustomObject]@{ Updates = @($u1) }
$searcher = [PSCustomObject]@{ ServerSelection = 0 }
$searcher | Add-Member -MemberType ScriptMethod -Name Search -Value { param($q) $searchResult } -Force
# A real ArrayList gives Add and a working Count; a ScriptMethod named get_Count
# does not bind as a property, and the script branches on $toDownload.Count.
# Built statically so it does not depend on the New-Object shim below.
$updateColl = [System.Collections.ArrayList]::new()
$downloader = [PSCustomObject]@{ Updates = @() }
$downloader | Add-Member -MemberType ScriptMethod -Name Download -Value { } -Force
$installResult = [PSCustomObject]@{ RebootRequired = $false; ResultCode = 2 }
$installer = [PSCustomObject]@{ Updates = @() }
$installer | Add-Member -MemberType ScriptMethod -Name Install -Value { $installResult } -Force
$session = [PSCustomObject]@{}
$session | Add-Member -MemberType ScriptMethod -Name CreateUpdateSearcher -Value { $searcher } -Force
$session | Add-Member -MemberType ScriptMethod -Name CreateUpdateDownloader -Value { $downloader } -Force
$session | Add-Member -MemberType ScriptMethod -Name CreateUpdateInstaller -Value { $installer } -Force
function New-Object {
  param(
    [Parameter(Position=0)][string]$Type,
    [string]$ComObject
  )
  # The leading comma stops PowerShell unrolling the ArrayList into the output
  # stream: an empty ArrayList emits nothing, which would make $toDownload null.
  if ($ComObject -like '*UpdateColl*') { return ,$updateColl }
  return $session
}
`
}

// TestInstallOSInstallsTheRequestedPatch is the regression test for the $pid bug
// in the installer. Assigning read-only $pid throws a terminating error, so
// `-contains $pid` can never match a KB article id, no update is ever selected,
// and the script falls through to "Simulated patch confirmation" and reports
// status "completed" without installing anything.
func TestInstallOSInstallsTheRequestedPatch(t *testing.T) {
	res, err := installOSWithPrefix(context.Background(), installShim(), InstallParams{
		JobID:    "job-1",
		PatchIDs: []string{"KB5007651"},
	})
	if err != nil {
		t.Fatalf("install returned error: %v", err)
	}
	if strings.Contains(res.OutputLog, "Simulated patch confirmation") {
		t.Fatalf("install reported a simulated confirmation instead of installing: %s", res.OutputLog)
	}
	if res.Status != "completed" {
		t.Errorf("status = %q, want %q; log: %s", res.Status, "completed", res.OutputLog)
	}
	if !strings.Contains(res.OutputLog, "Matched patch: KB5007651") {
		t.Errorf("log does not show the requested KB being matched: %s", res.OutputLog)
	}
}

// TestInstallOSNoMatchIsNotSuccess covers the fake success path directly: a
// target that matches nothing must be a failure, not a simulated "completed".
func TestInstallOSNoMatchIsNotSuccess(t *testing.T) {
	res, err := installOSWithPrefix(context.Background(), installShim(), InstallParams{
		JobID:    "job-2",
		PatchIDs: []string{"KB0000000"},
	})
	if err != nil {
		t.Fatalf("install returned error: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want %q: a target that matched no available update installed nothing and must not report success; log: %s", res.Status, "failed", res.OutputLog)
	}
}

// TestInstallOSExecutionFailureIsNotSuccess: a WUA failure must surface as
// failure even when the catch block has already appended a notice.
func TestInstallOSExecutionFailureIsNotSuccess(t *testing.T) {
	shim := strings.Replace(installShim(),
		"-Name Search -Value { param($q) $searchResult }",
		"-Name Search -Value { param($q) throw 'WUA service is unreachable (0x8024402C)' }", 1)
	res, err := installOSWithPrefix(context.Background(), shim, InstallParams{
		JobID:    "job-3",
		PatchIDs: []string{"KB5007651"},
	})
	if err != nil {
		t.Fatalf("install returned error: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want %q; log: %s", res.Status, "failed", res.OutputLog)
	}
	if res.ErrorMessage == "" {
		t.Error("ErrorMessage is empty: the operator gets a failure with no reason")
	}
}

// TestInstallScriptAssignsNoReadOnlyVariable is the static guard for the
// installer script, which is built with fmt.Sprintf rather than a constant.
func TestInstallScriptAssignsNoReadOnlyVariable(t *testing.T) {
	readOnly := []string{
		"pid", "args", "input", "this", "error", "host", "matches", "foreach",
		"switch", "lastExitCode", "PSItem", "PSCmdlet", "PSBoundParameters",
		"MyInvocation", "ExecutionContext", "HOME", "PWD",
	}
	for _, name := range readOnly {
		for _, pattern := range []string{"$" + name + " =", "$" + name + "=", "$" + name + " +="} {
			if strings.Contains(buildInstallScript("'KB1'"), pattern) {
				t.Errorf("install script assigns to $%s (matched %q), a read-only automatic PowerShell variable", name, pattern)
			}
		}
	}
}
