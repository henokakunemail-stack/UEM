//go:build windows

package software

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cover the cases an adversarial review found after the feature was
// already verified live. Each one is a way the "silent or nothing" promise could
// be broken while the code still returned no error, which is the only kind of
// failure that matters here: a refusal is loud, a mangled argv is quiet.
//
// All of them drive resolveSilentArgs, which the '!windows' build deliberately
// does not implement -- Linux and macOS uninstall through package managers that
// carry their own non-interactive switches, so there is nothing to resolve. The
// portable half of this review lives in uninstall_by_name_review_test.go.

// An UNQUOTED QuietUninstallString whose path contains spaces splits across
// several tokens. Comparing token[0] against the whole path never matches, so
// "C:\Program" and "Files\App\unins000.exe" used to be handed to the uninstaller
// as arguments -- and for a bare path with no switches, the length check below
// that guard saw two non-empty fragments and did not fire. The uninstaller would
// then run with no silent switch at all, which is precisely the interactive
// window this module refuses to cause.
func TestResolveSilentArgsRefusesAnUnquotedQuietCommandWithNoSwitches(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "Program Files", "App", "unins000.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Unquoted, and carrying no switches at all.
	args, err := resolveSilentArgs(&installedProgram{
		Name:            "Unquoted App",
		UninstallString: exe,
		QuietString:     exe,
	})
	if err == nil {
		t.Fatalf("resolveSilentArgs accepted an unquoted bare path and returned %v; "+
			"that runs the uninstaller with zero silent switches", args)
	}
	// Which of the two refusals fires is not the point -- here the strip succeeds
	// and leaves nothing, so the length check is what catches it. The point is
	// that nothing runs and the operator is told so.
	if !strings.Contains(err.Error(), "nothing was run") {
		t.Errorf("refusal should say nothing ran:\n%v", err)
	}
}

// A path that resolves to something SHORTER than the recorded text is the other
// shape the strip cannot handle, and the one that actually leaks: uninstallerPath
// scans for the longest existing prefix, so a machine where only "C:\Program"
// exists resolves there while the registry value continues "C:\Program Files\...".
// The prefix match consumes "C:\Program" and leaves the rest as arguments.
func TestResolveSilentArgsRefusesWhenOnlyAPathPrefixExists(t *testing.T) {
	root := t.TempDir()
	// The short path really is a file, so uninstallerPath resolves to it.
	short := filepath.Join(root, "Program")
	if err := os.WriteFile(short, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The full path the registry spells out does not exist.
	full := filepath.Join(root, "Program Files", "App", "unins000.exe")

	args, err := resolveSilentArgs(&installedProgram{
		Name:            "Prefix App",
		UninstallString: short,
		QuietString:     full + " /VERYSILENT",
	})
	if err == nil {
		t.Fatalf("resolveSilentArgs returned %v for a quiet command whose own path "+
			"could only be partly resolved; the fragments would go to the uninstaller", args)
	}
	if !strings.Contains(err.Error(), "could not be separated") {
		t.Errorf("refusal should name the path-separation problem:\n%v", err)
	}
}

// The same shape WITH switches must still work, minus the path fragments. If the
// fix had simply refused every unquoted value, this would regress coverage for a
// form the registry does write.
func TestResolveSilentArgsKeepsSwitchesFromAnUnquotedQuietCommand(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "Program Files", "App", "unins000.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}

	args, err := resolveSilentArgs(&installedProgram{
		Name:            "Unquoted App",
		UninstallString: exe,
		QuietString:     exe + " /VERYSILENT /SUPPRESSMSGBOXES",
	})
	if err != nil {
		t.Fatalf("an unquoted path with switches is a legitimate recorded quiet command: %v", err)
	}
	want := []string{"/VERYSILENT", "/SUPPRESSMSGBOXES"}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v: path fragments leaked into argv", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %v, want %v", args, want)
		}
	}
}

// The strip used to compare the quiet command against ITSELF -- parsed[0] versus
// uninstallerPath(quiet) -- and never against the executable that actually runs,
// which comes from UninstallString. A vendor that records a wrapper's switches
// would therefore pass "verified silent" while those switches went to a different
// program, which may well raise its own dialog.
func TestResolveSilentArgsRefusesAQuietCommandForADifferentBinary(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "unins000.exe")
	wrapper := filepath.Join(root, "wrapper.exe")
	for _, p := range []string{real, wrapper} {
		if err := os.WriteFile(p, []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	_, err := resolveSilentArgs(&installedProgram{
		Name:            "Wrapped App",
		UninstallString: real,
		QuietString:     wrapper + " /VERYSILENT /SUPPRESSMSGBOXES",
	})
	if err == nil {
		t.Fatal("resolveSilentArgs granted a silent verdict on switches belonging to a " +
			"different executable than the one runPlatformUninstall launches")
	}
	if !strings.Contains(err.Error(), "different executable") {
		t.Errorf("refusal should explain the mismatch:\n%v", err)
	}
}
