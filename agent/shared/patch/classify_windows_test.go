//go:build windows

package patch

import (
	"context"
	"strings"
	"testing"
)

// These cover the classification the scan added after it stopped filtering the
// search to Type='Software'. Every test drives the real script through the
// existing COM shim, so the PowerShell branching is what is under test, not a
// Go re-implementation of it.

// searchOf returns a shim whose fake WUA service answers the given update list.
func searchOf(updates string) string {
	return `$u = ` + updates + `
$searchResult = [PSCustomObject]@{ Updates = @($u) }
$searcher = [PSCustomObject]@{ ServerSelection = 0 }
$searcher | Add-Member -MemberType ScriptMethod -Name Search -Value { param($q) $searchResult } -Force
$session = [PSCustomObject]@{}
$session | Add-Member -MemberType ScriptMethod -Name CreateUpdateSearcher -Value { $searcher } -Force
$list = @()
try {
    $result = $searcher.Search("IsInstalled=0")
`
}

// update builds a fake WUA update. The props argument carries every field a
// test asserts on -- Type, IsHidden, Categories and Title -- because PowerShell
// rejects a hash literal with a duplicate key, so a value cannot be defaulted
// here and overridden there.
func update(props string) string {
	return `[PSCustomObject]@{ KBArticleIDs = @(); MsrcSeverity = 'Important'; ` +
		`Description = ''; RebootRequired = $false; ` +
		`Identity = [PSCustomObject]@{ UpdateID = 'GUID-X' }; ` + props + ` }`
}

// A driver update is what made the reported count wrong: WUA held eight on the
// machine that was shown as having three. Type 2 is the field that says so.
func TestDriverUpdateIsClassifiedAsDriver(t *testing.T) {
	items, err := runScanScript(context.Background(),
		searchOf(update(`Type = 2; IsHidden = $false; Title = 'Goodix Biometric Driver Update'; Categories = @([PSCustomObject]@{ Name = 'Drivers' })`))+scanBody(t))
	if err != nil {
		t.Fatalf("scan returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d patches, want 1", len(items))
	}
	if items[0].Category != CategoryDriver {
		t.Errorf("Category = %q, want %q: a Type 2 update is a driver whatever its category strings say", items[0].Category, CategoryDriver)
	}
	// Drivers carry no KBArticleIDs at all, so the row's identity is the GUID
	// and kb_id is empty. The console has to be able to display that.
	if items[0].KBID != "" {
		t.Errorf("KBID = %q, want empty", items[0].KBID)
	}
	if items[0].PatchID == "" {
		t.Error("PatchID is empty: a driver with no KB must fall back to its UpdateID")
	}
}

// A deferred update is hidden from Settings but is still pending. Reporting it
// would add a row that never clears, because the next scan skips it.
func TestHiddenUpdateIsSkipped(t *testing.T) {
	items, err := runScanScript(context.Background(),
		searchOf(update(`Type = 1; IsHidden = $true; Title = 'Deferred by admin'; Categories = @()`))+scanBody(t))
	if err != nil {
		t.Fatalf("scan returned error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("got %d patches, want 0: a hidden update is deferred by policy and must not be reported as pending", len(items))
	}
}

// Categories[0] was the whole classification. KB890830's first category is "EU
// Browser Choice Update-For Europe Only" while the security category is the
// second, so the old code filed a malware-removal tool under updates.
func TestSecurityCategoryIsFoundBeyondTheFirst(t *testing.T) {
	cats := `@(
        [PSCustomObject]@{ Name = 'EU Browser Choice Update-For Europe Only' },
        [PSCustomObject]@{ Name = 'Windows 10' },
        [PSCustomObject]@{ Name = 'Security Updates' }
    )`
	items, err := runScanScript(context.Background(),
		searchOf(update(`Type = 1; IsHidden = $false; Title = 'Malware Removal Tool'; Categories = `+cats))+scanBody(t))
	if err != nil {
		t.Fatalf("scan returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d patches, want 1", len(items))
	}
	if items[0].Category != CategorySecurity {
		t.Errorf("Category = %q, want %q: only the first category was consulted", items[0].Category, CategorySecurity)
	}
}

// The install query has to match the scan query. With the scan widened and the
// installer left on Type='Software', a driver appeared in the console and then
// failed to install with "no available update matched".
func TestInstallScriptSearchesEveryUpdateType(t *testing.T) {
	script := buildInstallScript("'KB1'")
	if !strings.Contains(script, `Search("IsInstalled=0")`) {
		t.Errorf("install script does not use the widened query:\n%s", script)
	}
	if strings.Contains(script, "Type='Software'") {
		t.Error("install script still restricts the search to software, so a driver the scan reported can never be installed")
	}
}
