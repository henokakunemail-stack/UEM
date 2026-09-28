//go:build windows

package software

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The defect this file exists for: install_args used to go through
// strings.Fields, which splits `/DIR="C:\Program Files\App"` into two arguments
// and installs the product somewhere the operator never asked for. The cases
// below guard the exact boundary where the naive fix regresses.
func TestSplitArgsKeepsQuotedPathsWhole(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "quoted path with spaces stays one argument",
			in:   `/DIR="C:\Program Files\App"`,
			want: []string{`/DIR=C:\Program Files\App`},
		},
		{
			name: "several switches",
			in:   `/VERYSILENT /SUPPRESSMSGBOXES /DIR="C:\Program Files\App"`,
			want: []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", `/DIR=C:\Program Files\App`},
		},
		{
			name: "winrar sfx",
			in:   "/s",
			want: []string{"/s"},
		},
		{
			name: "unquoted path is untouched",
			in:   `/D=C:\Temp`,
			want: []string{`/D=C:\Temp`},
		},
		{
			name: "a quoted path with no switch",
			in:   `"C:\Program Files\App\setup.exe"`,
			want: []string{`C:\Program Files\App\setup.exe`},
		},
		{
			name: "tab separated switches",
			in:   "/S\t/D=C:\\Temp",
			want: []string{"/S", `/D=C:\Temp`},
		},
		{
			name: "empty is no arguments",
			in:   "",
			want: nil,
		},
		{
			name: "whitespace only is no arguments",
			in:   "   ",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitArgs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitArgs(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("splitArgs(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestArgProbeHelper is not a real test. Re-executing the test binary with this
// name selected makes it a program that prints its own argv, which is the only
// authority on what a real Windows installer will see.
func TestArgProbeHelper(t *testing.T) {
	if os.Getenv("EPM_ARG_PROBE") != "1" {
		t.Skip("helper, only runs when re-executed by the probe test")
	}
	for i, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-test.") {
			continue
		}
		fmt.Printf("EPMARG %d %s\n", i, a)
	}
	os.Exit(0)
}

// The real authority is a running process, not our reading of the rules. This
// re-executes the test binary and compares the argv it actually received
// against what splitArgs predicted. If the two ever diverge, the installer on
// the endpoint sees something other than what the operator typed.
func TestSplitArgsMatchesARealProcessArgv(t *testing.T) {
	cases := []string{
		`/DIR="C:\Program Files\App"`,
		`/S /D="C:\Program Files\App Data"`,
		`"C:\Program Files\App\setup.exe" /S`,
		`/A "x y" /B z`,
		`/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /DIR="C:\Program Files\App"`,
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot locate the test binary: %v", err)
	}

	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			want := splitArgs(in)
			if len(want) == 0 {
				t.Fatalf("splitArgs(%q) produced nothing to test", in)
			}

			cmd := exec.Command(exe, append([]string{"-test.run=TestArgProbeHelper"}, want...)...)
			cmd.Env = append(os.Environ(), "EPM_ARG_PROBE=1")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("probe failed: %v: %s", err, out)
			}
			got := parseProbeOutput(string(out))

			if len(got) != len(want) {
				t.Fatalf("probe received %d args, splitArgs predicted %d\nprobe: %#v\npred: %#v",
					len(got), len(want), got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("arg %d: probe got %q, splitArgs predicted %q", i, got[i], want[i])
				}
			}
		})
	}
}

// An .exe with no silent switches used to be launched bare, which pops a GUI
// on the endpoint's desktop and never returns. The runner must refuse instead.
//
// This lives in a windows-tagged file because windowsRunner is defined in
// runner_windows.go. With the test in the shared file, `GOOS=linux go vet` --
// which is what CI runs on ubuntu-latest -- failed to compile the package.
func TestWindowsRunnerRefusesBareExe(t *testing.T) {
	_, _, err := (&windowsRunner{}).Run(t.Context(), "C:\\tmp\\setup.exe", "exe", "")
	if err == nil {
		t.Fatal("expected an error for an .exe with no silent-install arguments")
	}
	if !strings.Contains(err.Error(), "silent") {
		t.Errorf("error should name the missing silent arguments, got: %v", err)
	}
}

// Whitespace-only install_args slipped past the empty-string guard and
// launched the installer bare, reopening exactly the GUI popup the test above
// exists to prevent. The emptiness test has to be on the parsed slice.
func TestWindowsRunnerRefusesNonArguments(t *testing.T) {
	for _, args := range []string{"", " ", "   ", "\t", "  \t "} {
		t.Run("args="+strconv.Quote(args), func(t *testing.T) {
			_, _, err := (&windowsRunner{}).Run(t.Context(), "C:\\tmp\\setup.exe", "exe", args)
			if err == nil {
				t.Fatalf("expected a refusal for install_args = %q", args)
			}
			if !strings.Contains(err.Error(), "silent") {
				t.Errorf("error should name the missing silent arguments, got: %v", err)
			}
		})
	}
}

// parseProbeOutput reads the helper's tagged lines back into a slice, in the
// order they were printed. The index is not assumed to start at zero: the
// helper counts every os.Args entry, so the -test.run flag shifts it.
func parseProbeOutput(out string) []string {
	var args []string
	for _, line := range strings.Split(out, "\n") {
		_, rest, ok := strings.Cut(strings.TrimSpace(line), "EPMARG ")
		if !ok {
			continue
		}
		idx, value, ok := strings.Cut(rest, " ")
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(idx); err != nil {
			continue
		}
		args = append(args, value)
	}
	return args
}
