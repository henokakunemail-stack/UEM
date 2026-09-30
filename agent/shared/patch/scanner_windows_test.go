//go:build windows

package patch

import (
	"context"
	"strings"
	"testing"
)

// winScanScriptPrefix is the exact head of winScanScript: it creates the COM
// session, runs the search, and opens the try block. Tests replace this head
// with a shim so the loop, the result marshalling and the Go parser -- i.e. all
// the code under test -- run verbatim against a fake Windows Update service.
const winScanScriptPrefix = `
$ErrorActionPreference = 'Stop'
$payload = $null
try {
    $session = New-Object -ComObject Microsoft.Update.Session
    $searcher = $session.CreateUpdateSearcher()
    $searcher.ServerSelection = 2
    $result = $searcher.Search("IsInstalled=0 and Type='Software'")
`

// scanBody returns winScanScript with its session-creating head stripped, i.e.
// the loop through to the closing brace of the try block.
func scanBody(t *testing.T) string {
	t.Helper()
	if !strings.HasPrefix(winScanScript, winScanScriptPrefix) {
		t.Fatalf("winScanScript no longer starts with the expected prefix; update winScanScriptPrefix in this test to match. Head was:\n%s", winScanScript[:min(len(winScanScript), 300)])
	}
	return winScanScript[len(winScanScriptPrefix):]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// comShim shadows the New-Object cmdlet with a function so the script talks to a
// fake session instead of the real Windows Update service. The function must be
// defined before the body runs: powershell.exe resolves New-Object by function
// precedence over the cmdlet.
//
// Session/Searcher are PSCustomObjects carrying ScriptMethod members -- a
// NoteProperty holding a scriptblock fails with "does not contain a method
// named" -- and a real writable ServerSelection property, because the script
// assigns to it.
//
// $u1 has a KB article and a Security category; $u2 has an empty KBArticleIDs
// and no categories, so the null-guarded property paths are exercised.
func comShim(searchBody string) string {
	cat := `[PSCustomObject]@{ Name = 'Security Updates' }`
	u1 := `[PSCustomObject]@{
        KBArticleIDs   = @('5007651')
        MsrcSeverity   = 'Critical'
        Categories     = @(` + cat + `)
        Title          = 'Cumulative Update for Windows'
        Description    = 'A security update.'
        RebootRequired = $false
        Identity       = [PSCustomObject]@{ UpdateID = 'GUID-A' }
    }`
	u2 := `[PSCustomObject]@{
        KBArticleIDs   = @()
        MsrcSeverity   = 'Low'
        Categories     = @()
        Title          = 'Optional .NET update'
        Description    = ''
        RebootRequired = $true
        Identity       = [PSCustomObject]@{ UpdateID = 'GUID-B' }
    }`
	return `$u1 = ` + u1 + `
$u2 = ` + u2 + `
# Updates is a plain data property, not a method: a ScriptMethod would unroll the
# returned array into the output stream and the script would see one element.
$searchResult = [PSCustomObject]@{ Updates = @($u1, $u2) }
$searcher = [PSCustomObject]@{ ServerSelection = 0 }
$searcher | Add-Member -MemberType ScriptMethod -Name Search -Value ` + searchBody + ` -Force
$session = [PSCustomObject]@{}
$session | Add-Member -MemberType ScriptMethod -Name CreateUpdateSearcher -Value { $searcher } -Force
$list = @()
try {
    $result = $searcher.Search("IsInstalled=0 and Type='Software'")
`
}

// twoUpdates returns a shim whose Search yields two fake available updates.
func twoUpdates() string {
	return comShim(`{ param($q) $searchResult }`)
}

// throwingSearch returns a shim whose Search fails the way an unreachable WUA
// service does.
func throwingSearch() string {
	return comShim(`{ param($q) throw 'WUA service is unreachable (0x8024402C)' }`)
}

// emptySearch returns a shim whose Search yields an up-to-date device.
func emptySearch() string {
	return comShim(`{ param($q) [PSCustomObject]@{ Updates = @() } }`)
}

// TestScanScriptReturnsEveryAvailableUpdate is the core regression test for the
// $pid bug. Before the fix, assigning the read-only automatic $pid throws a
// terminating error on the first update, the script's catch emits "[]", and the
// agent logs "patch scan reported successfully" with count 0 on a device that
// has updates pending.
func TestScanScriptReturnsEveryAvailableUpdate(t *testing.T) {
	items, err := runScanScript(context.Background(), twoUpdates()+scanBody(t))
	if err != nil {
		t.Fatalf("scan returned error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d patches, want 2: the scan reported an empty result for a device with 2 available updates (assigning read-only $pid aborts the loop and the catch emits a fake empty list)", len(items))
	}
	if items[0].PatchID != "KB5007651" {
		t.Errorf("items[0].PatchID = %q, want %q", items[0].PatchID, "KB5007651")
	}
	if items[1].PatchID != "GUID-B" {
		t.Errorf("items[1].PatchID = %q, want %q (an update with no KBArticleIDs must fall back to the UpdateID)", items[1].PatchID, "GUID-B")
	}
	if items[0].Severity != "critical" {
		t.Errorf("items[0].Severity = %q, want %q", items[0].Severity, "critical")
	}
	if items[1].Severity != "low" {
		t.Errorf("items[1].Severity = %q, want %q", items[1].Severity, "low")
	}
	if items[0].RebootRequired {
		t.Error("items[0].RebootRequired = true, want false")
	}
	if !items[1].RebootRequired {
		t.Error("items[1].RebootRequired = false, want true")
	}
	if items[0].Category != "security" {
		t.Errorf("items[0].Category = %q, want %q", items[0].Category, "security")
	}
}

// TestScanScriptFailureIsNotZeroUpdates is the regression test for the
// catch { Write-Output "[]" } fake-success path: a broken scan must surface as
// a failure, never as a compliant device.
func TestScanScriptFailureIsNotZeroUpdates(t *testing.T) {
	items, err := runScanScript(context.Background(), throwingSearch()+scanBody(t))
	if err == nil {
		t.Fatalf("got err = nil and %d patches, want a non-nil error: a failed Windows Update scan is currently reported as a successful scan of zero patches", len(items))
	}
	if !strings.Contains(err.Error(), "0x8024402C") {
		t.Errorf("err = %v, want it to name the underlying cause (0x8024402C)", err)
	}
}

// TestScanScriptZeroUpdatesIsNotAFailure guards the opposite error: an
// up-to-date device must stay a clean success with zero items, not an error.
func TestScanScriptZeroUpdatesIsNotAFailure(t *testing.T) {
	items, err := runScanScript(context.Background(), emptySearch()+scanBody(t))
	if err != nil {
		t.Fatalf("a device with no available updates must not be an error, got: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("got %d patches, want 0", len(items))
	}
}

// TestScanScriptAssignsNoReadOnlyVariable is a static guard against reintroducing
// the original defect without needing a live WUA service to notice. Only genuinely
// read-only AUTOMATIC variables belong here: $ErrorActionPreference is a writable
// preference variable and is deliberately not listed.
func TestScanScriptAssignsNoReadOnlyVariable(t *testing.T) {
	readOnly := []string{
		"pid", "args", "input", "this", "error", "host", "matches", "foreach",
		"switch", "lastExitCode", "PSItem", "PSCmdlet", "PSBoundParameters",
		"MyInvocation", "ExecutionContext", "HOME", "PWD", "ShellId",
		"ConsoleFileName", "Profile",
	}
	for _, name := range readOnly {
		for _, pattern := range []string{"$" + name + " =", "$" + name + "=", "$" + name + " +="} {
			if strings.Contains(winScanScript, pattern) {
				t.Errorf("winScanScript assigns to $%s (matched %q), a read-only automatic PowerShell variable: the assignment throws a terminating error and the scan reports a fake empty result", name, pattern)
			}
		}
	}
}
