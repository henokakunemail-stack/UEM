package software

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The cases an adversarial review found after the feature was already verified
// live, the ones that do not depend on the Windows-only switch resolution. The
// rest are in uninstall_by_name_review_windows_test.go, gated to windows for the
// same reason silent_uninstall_unix.go does not implement them.

// "refused" was the label for every failure, including an uninstaller that ran
// and exited 1603. That discards msiexec's own diagnostics, which runProcess
// kept a bounded tail of for exactly this purpose, and this row is the only
// record an operator gets.
func TestDescribeUninstallKeepsTheOutputOfARealFailure(t *testing.T) {
	text := DescribeUninstall("Broken App", 1603, "Error 1603. Fatal error during installation", errors.New("exit status 1603"))

	if !strings.Contains(text, "1603") {
		t.Errorf("a failed run must carry its exit code:\n%s", text)
	}
	if !strings.Contains(text, "Fatal error during installation") {
		t.Errorf("the uninstaller's own diagnostics must survive:\n%s", text)
	}
	if strings.Contains(text, "refused") {
		t.Errorf("an uninstaller that ran and failed was not refused by this code:\n%s", text)
	}
}

// A refusal never launched anything, so it carries -1 and has output to spare.
// Labelling it "failed (exit code -1)" reads like a broken program rather than
// the fixable thing it actually is.
func TestDescribeUninstallStillCallsANothingRanRefusalARefusal(t *testing.T) {
	text := DescribeUninstall("PostgreSQL 16", -1, "",
		errors.New("records no QuietUninstallString, so nothing was run"))

	if !strings.Contains(text, "refused") {
		t.Errorf("a refusal should say so, so an operator knows the program is untouched:\n%s", text)
	}
}

// The catalog path lost its absent-package sentence to a blanket error return
// sitting above the branch that used to produce it, so an already-compliant
// endpoint reported exit -1 with a blank output log.
func TestUninstallReportsAnAbsentPackageWithItsReason(t *testing.T) {
	// No withElevation call, on purpose: this is the catalog path, which takes its
	// arguments from a package record a human wrote and so has no reason to check
	// the agent's elevation. Only UninstallByName does, because only it has to
	// work out the switches itself and therefore cannot ask the operator.
	//
	// A name no registry scan can match, so ErrNotInstalled is what comes back.
	name := "ZZZ Definitely Not Installed " + t.TempDir()
	code, output, err := Uninstall(context.Background(), UninstallPayload{
		PackageName:   name,
		UninstallArgs: "/S",
	})
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v, want ErrNotInstalled", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0: an absent package is not a failure", code)
	}
	if !strings.Contains(output, "not installed") {
		t.Errorf("the operator-facing reason must survive, it is the whole message:\n%q", output)
	}
}
