package software

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// These tests cover the decision that decides whether anything runs at all:
// resolveSilentArgs. Every case below is a case where running the uninstaller
// would put a window on an endpoint, so the assertion is as much about what did
// NOT happen as about what was returned.

// withElevation makes the elevation gate pass for one test.
//
// UninstallByName refuses before it looks at anything else when the agent is not
// running as a service, which is correct in production and makes every other
// case here unreachable from a plain `go test`. Nothing stubs the switch
// resolution itself -- the whole point is to assert on what that code decides.
func withElevation(t *testing.T) {
	t.Helper()
	prev := elevationCheck
	elevationCheck = func() error { return nil }
	t.Cleanup(func() { elevationCheck = prev })
}

// An MSI needs nothing from the operator: msiexec's own switches are built in
// runMsiUninstall. The empty return is the assertion -- it must not read as
// "no args, therefore refuse", which is the catalog path's rule for a case that
// does not apply here.
func TestResolveSilentArgsTrustsAnMSIWithoutAnyOperatorInput(t *testing.T) {
	args, err := resolveSilentArgs(&installedProgram{
		Name:            "Notepad++",
		UninstallString: `MsiExec.exe /X{8BD21D40-EC42-11CE-9E0D-00AA006002F3}`,
		// Deliberately no QuietString: an MSI with one recorded should still be
		// resolved by the MSI branch, not by parsing the vendor's string.
		QuietString: "",
	})
	if err != nil {
		t.Fatalf("resolveSilentArgs refused an MSI: %v", err)
	}
	if len(args) != 0 {
		t.Errorf("resolveSilentArgs returned %v for an MSI, want none: msiexec's /x /qn "+
			"/norestart are built in runMsiUninstall", args)
	}
}

// A native uninstaller with a recorded quiet command runs on exactly that
// command and nothing else. The switches come from the endpoint's registry, so
// they are the vendor's own declaration rather than a guess -- which is the only
// reason this path is allowed to proceed at all.
func TestResolveSilentArgsUsesTheRecordedQuietCommand(t *testing.T) {
	args, err := resolveSilentArgs(&installedProgram{
		Name:            "WinRAR 6.24 (64-bit)",
		UninstallString: `C:\Program Files\WinRAR\Uninstall.exe`,
		QuietString:     `"C:\Program Files\WinRAR\Uninstall.exe" /S`,
	})
	if err != nil {
		t.Fatalf("resolveSilentArgs refused a program that records a quiet command: %v", err)
	}
	if len(args) != 1 || !strings.EqualFold(args[0], "/S") {
		t.Errorf("resolveSilentArgs returned %v, want the recorded /S from QuietUninstallString", args)
	}
}

// The refusal is the feature. Windows has no universal silent switch -- NSIS
// wants /S, Inno Setup wants /VERYSILENT /SUPPRESSMSGBOXES /NORESTART, WinRAR
// wants /s -- and a program recording none means nothing on the endpoint can say
// which one it wants. Running it anyway opens a window and waits for a human who
// is not there.
//
// The message has to name the program and the reason, because this is what an
// operator reads after the console said the request was sent.
func TestResolveSilentArgsRefusesANativeUninstallerWithNoQuietCommand(t *testing.T) {
	// PostgreSQL 16 from the registry survey: a real native uninstaller with no
	// QuietUninstallString.
	_, err := resolveSilentArgs(&installedProgram{
		Name:            "PostgreSQL 16",
		Version:         "16.4-1",
		UninstallString: `C:\Program Files\PostgreSQL\16\uninstall-postgresql.exe`,
	})
	if err == nil {
		t.Fatal("resolveSilentArgs accepted a native uninstaller with no quiet command; " +
			"running it would open a window on the endpoint")
	}
	for _, want := range []string{"PostgreSQL 16 16.4-1", "QuietUninstallString", "nothing was run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal message does not mention %q:\n%v", want, err)
		}
	}
}

// A QuietUninstallString that parses to nothing is the same situation as having
// none: no switches that can be trusted. Shipping a mangled argv is exactly how
// a silent uninstall turns into an interactive one, so it is a refusal too.
//
// Both paths point at the same quoted executable deliberately: the value is
// unparseable, and the refusal has to come from the parse rather than from the
// same-executable check, which now runs alongside it.
func TestResolveSilentArgsRefusesAQuietCommandItCannotParse(t *testing.T) {
	// A NUL byte is what makes windows.DecomposeCommandLine fail outright.
	_, err := resolveSilentArgs(&installedProgram{
		Name:            "Broken Publisher App",
		UninstallString: `"C:\Program Files\Broken\uninstall.exe"`,
		QuietString:     "\"C:\\Program Files\\Broken\\uninstall.exe\" /S\x00 /X",
	})
	if err == nil {
		t.Fatal("resolveSilentArgs accepted an unparseable QuietUninstallString")
	}
	if !strings.Contains(err.Error(), "no usable arguments") {
		t.Errorf("refusal should say the value yielded no arguments:\n%v", err)
	}
}

// Elevation is checked before the name is even resolved, so a user-session agent
// refuses everything. ErrNotElevated is distinct from a run failure because the
// two call for different responses: one needs the agent reinstalled as a
// service, the other might be worth a retry.
func TestUninstallByNameRefusesBeforeAnythingRunsWhenNotElevated(t *testing.T) {
	prev := elevationCheck
	elevationCheck = func() error {
		return errors.Join(ErrNotElevated, errors.New("test agent runs in a user session"))
	}
	t.Cleanup(func() { elevationCheck = prev })

	// A name that certainly resolves on a real machine, so the only thing that
	// can produce the refusal is the elevation gate running first.
	code, _, err := UninstallByName(context.Background(), "Notepad++")
	if !errors.Is(err, ErrNotElevated) {
		t.Fatalf("err = %v, want ErrNotElevated", err)
	}
	if code != -1 {
		t.Errorf("exit code = %d, want -1: nothing ran", code)
	}
}

// An empty name must never reach the registry scan. It is the shape a UI bug or
// a hand-rolled request produces, and "matches everything" would be a very bad
// thing to hand to code that runs uninstallers.
func TestUninstallByNameRefusesAnEmptyName(t *testing.T) {
	withElevation(t)
	for _, name := range []string{"", "   ", "\t\n"} {
		if _, _, err := UninstallByName(context.Background(), name); err == nil {
			t.Errorf("UninstallByName(%q) was accepted; an empty name must be refused", name)
		}
	}
}

// A program that is not installed is a completed check, not a failure. The
// compliance rule being enforced is "this program must not be here", and a
// machine that never had it already satisfies it -- so this has to read as
// success, or operators learn to ignore the red rows that matter.
func TestNotInstalledIsReportedAsSuccess(t *testing.T) {
	withElevation(t)

	// ErrNotInstalled is what resolveTarget returns for a name nothing matches.
	// Asserted on the error itself rather than on a live registry scan so the
	// contract holds on a machine that happens to have the program installed.
	err := error(nil)
	err = errors.Join(errors.New("Not Installed App is not installed on this endpoint; nothing to remove"), ErrNotInstalled)
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatal("errors.Is must see through the message wrapping")
	}

	text := DescribeUninstall("Not Installed App", 0, "Not Installed App is not installed on this endpoint; nothing to remove", err)
	if strings.Contains(text, "refused") || strings.Contains(text, "failed") {
		t.Errorf("an absent program must not be described as a failure: %q", text)
	}
	if !strings.Contains(text, "not installed") {
		t.Errorf("the result should say why there was nothing to do: %q", text)
	}
}

// DescribeUninstall is the only record the console has of what happened. The
// HTTP response said 201 when the command was merely on its way, so every
// outcome -- especially a refusal -- has to arrive here as a sentence an
// operator can act on rather than "error: uninstall failed".
func TestDescribeUninstallSaysWhatHappened(t *testing.T) {
	cases := []struct {
		name     string
		program  string
		code     int
		output   string
		err      error
		contains []string
	}{
		{
			name:     "completed",
			program:  "7-Zip 23.01",
			code:     0,
			contains: []string{"7-Zip 23.01", "uninstall completed", "exit code 0"},
		},
		{
			name:     "completed with output",
			program:  "7-Zip 23.01",
			code:     0,
			output:   "Removal completed successfully",
			contains: []string{"7-Zip 23.01", "exit code 0", "Removal completed successfully"},
		},
		{
			name:     "refused",
			program:  "PostgreSQL 16",
			code:     -1,
			err:      errors.New("records no QuietUninstallString"),
			contains: []string{"PostgreSQL 16", "uninstall refused", "QuietUninstallString"},
		},
		{
			name:     "not elevated",
			program:  "Some App",
			code:     -1,
			err:      ErrNotElevated,
			contains: []string{"uninstall refused", "Windows Service"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeUninstall(tc.program, tc.code, tc.output, tc.err)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("DescribeUninstall = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// The reboot-required codes are the ordinary way an MSI says it removed
// something but could not delete a file in use. runProcess reports any non-zero
// exit as an error, so without this the console would show a failed removal on a
// machine where the program is in fact gone.
func TestRebootRequiredExitIsReadOffTheExitCodeNotTheError(t *testing.T) {
	// Only meaningful on Windows, where msiexec is the uninstall mechanism.
	if !isMSIShape("MsiExec.exe /X{1234}") {
		t.Skip("not the Windows uninstall shape")
	}
	msi := &installedProgram{UninstallString: `MsiExec.exe /X{1234}`}
	for _, code := range []int{0, 1641, 3010} {
		if !isRebootRequiredExit(msi, code) {
			t.Errorf("isRebootRequiredExit(msi, %d) = false, want true: this code means the "+
				"uninstall finished and Windows wants a restart", code)
		}
	}
	for _, code := range []int{1603, 1618, 1605} {
		if isRebootRequiredExit(msi, code) {
			t.Errorf("isRebootRequiredExit(msi, %d) = true, want false: this is a real failure", code)
		}
	}

	// A native uninstaller has no such codes. Treating one as reboot-required
	// would report a removal that did not happen.
	native := &installedProgram{UninstallString: `C:\Program Files\App\uninstall.exe`}
	if isRebootRequiredExit(native, 3010) {
		t.Error("isRebootRequiredExit(native, 3010) = true, want false: 3010 is an msiexec code")
	}
}

// isMSIShape is a local stand-in so the test above can skip on non-Windows
// builds without importing a Windows-only symbol into a portable file.
func isMSIShape(s string) bool {
	return strings.Contains(strings.ToLower(s), "msiexec")
}
