package software

import (
	"os"
	"path/filepath"
	"testing"
)

// These are the tests that decide whether an uninstall removes the program the
// operator named or a different one. There is no undo, so the cases below are
// the ones that have to be right rather than the ones that are convenient.

// A name match decides what gets removed. A miss costs an operator retrying; a
// false positive removes a program nobody named. So the generous case and the
// strict case are both pinned here.
func TestNameMatches(t *testing.T) {
	cases := []struct {
		query     string
		candidate string
		want      bool
	}{
		// Exact after normalisation.
		{"winrar", "winrar", true},
		{"WinRAR", "WinRAR", true},
		{"7-Zip", "7zip", true},
		{"7-zip", "7-Zip 23.01 (x64)", true},

		// The survey case: the registry spells it with a version and a bit
		// width, the catalog does not.
		{"winrar", "WinRAR 7.23 (64-bit)", true},
		{"WinRAR archiver", "WinRAR 7.23 (64-bit)", false},

		// Multi-word query, every word present.
		{"Adobe Acrobat", "Adobe Acrobat Reader DC", true},
		{"adobe acrobat reader", "Adobe Acrobat Reader DC (2015)", true},
		{"Adobe Photoshop", "Adobe Acrobat Reader DC", false},

		// Word boundaries, not substrings. "zip" matching "Gzip Expert" is
		// precisely the kind of false positive that has no undo.
		{"zip", "Gzip Expert", false},
		{"zip", "7-Zip 23.01", false},
		{"7-Zip", "WinZip 21.0", false},

		// No reverse matching: a broad query must not collapse two distinct
		// products into one match. The caller reports the ambiguity instead.
		{"Acrobat", "Adobe Acrobat Reader DC", false},
		{"Acrobat", "Adobe Acrobat DC", false},

		// Empty on either side never matches.
		{"", "WinRAR", false},
		{"winrar", "", false},
		{"", "", false},
		{"   ", "WinRAR", false},
		{"winrar", "   ", false},

		// Punctuation-only query normalises to nothing and must not match.
		{"---", "WinRAR", false},
	}

	for _, tc := range cases {
		if got := nameMatches(tc.query, tc.candidate); got != tc.want {
			t.Errorf("nameMatches(%q, %q) = %v, want %v", tc.query, tc.candidate, got, tc.want)
		}
	}
}

// exactMatches is the tie-break that makes an exact query beat a longer name
// that starts with it. The case is real, not hypothetical: a registry survey
// found 47 entries on one workstation, among them both "Microsoft Edge" and
// "Microsoft Edge WebView2 Runtime". Without this, uninstalling Edge is
// impossible there -- both match, the ambiguity check fires, and the operator is
// told to narrow a query that is already exact.
func TestExactMatchesBeatsLongerPrefix(t *testing.T) {
	found := []*installedProgram{
		{Name: "Microsoft Edge"},
		{Name: "Microsoft Edge WebView2 Runtime"},
	}

	exact := exactMatches(found, "Microsoft Edge")
	if len(exact) != 1 {
		t.Fatalf("exactMatches returned %d programs, want 1", len(exact))
	}
	if exact[0].Name != "Microsoft Edge" {
		t.Errorf("exactMatches picked %q, want %q", exact[0].Name, "Microsoft Edge")
	}

	// A query that is not itself an installed name must not narrow anything, or
	// it would silently choose between candidates the operator never ranked.
	// The Python sub-components on the same machine are the case: "Python
	// 3.12.10" is not any of their exact names, so all ten stay and the
	// ambiguity is reported.
	py := []*installedProgram{
		{Name: "Python 3.12.10 (64-bit)"},
		{Name: "Python 3.12.10 pip Bootstrap (64-bit)"},
	}
	if got := exactMatches(py, "Python 3.12.10"); len(got) != 0 {
		t.Errorf("exactMatches narrowed %d candidates, want 0", len(got))
	}
}

// A name that is only a component of two different products must reach the
// ambiguity check, not resolve to one of them. These two are both on the
// workstation the registry survey ran against.
func TestComponentNameLeavesBothCandidates(t *testing.T) {
	found := []*installedProgram{
		{Name: "Microsoft Teams"},
		{Name: "Microsoft Teams Meeting Add-in for Microsoft Office"},
	}
	if got := exactMatches(found, "Microsoft Teams"); len(got) != 1 || got[0].Name != "Microsoft Teams" {
		t.Fatalf("exactMatches = %v, want just Microsoft Teams", got)
	}
	// And the versioned form has no exact match, so both remain.
	if got := exactMatches(found, "Microsoft Teams 2.0"); len(got) != 0 {
		t.Errorf("exactMatches = %v, want none", got)
	}
}

// uninstallerPath extracts only the executable from a recorded UninstallString.
// The switches belong to whatever wrote the registry entry, and running them is
// how a silent uninstall turns into an interactive one.
//
// The unquoted cases run against real files on disk, because the whole problem
// is that an unquoted path with spaces cannot be split without knowing where it
// ends, and the resolution is to look for a prefix that exists. Testing that
// against strings alone would pass for an implementation that has the bug.
func TestUninstallerPath(t *testing.T) {
	// A directory tree that reproduces the real shape, including a directory
	// that exists at a shorter prefix so the scan cannot just take the first
	// space and be lucky.
	dir := t.TempDir()
	appDir := filepath.Join(dir, "Program Files", "WinRAR")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(appDir, "Uninstall.exe")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	// A decoy that is a prefix cut at an earlier space: if the implementation
	// scans left-to-right it finds this and returns a directory.
	if err := os.WriteFile(filepath.Join(dir, "Program"), nil, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			// The WinRAR shape on this machine: unquoted, spaces, no switches.
			// Splitting on the first space here yields a path that is not a file.
			name:  "unquoted bare path with a space",
			input: exe,
			want:  exe,
		},
		{
			name:  "unquoted path with a trailing switch",
			input: exe + " /X",
			want:  exe,
		},
		{
			// The recorded switch looks like a file with an .exe extension
			// nowhere near it; the longest-existing-prefix rule cannot be fooled
			// by it because it does not exist.
			name:  "unquoted path with several switches",
			input: exe + " /X /S",
			want:  exe,
		},
		{
			name:  "quoted path with a switch",
			input: `"C:\Program Files\App\uninstall.exe" /X`,
			want:  `C:\Program Files\App\uninstall.exe`,
		},
		{
			name:  "quoted path with several switches",
			input: `"C:\Program Files\App\uninstall.exe" /X /S`,
			want:  `C:\Program Files\App\uninstall.exe`,
		},
		{
			name:  "surrounding whitespace",
			input: "  " + exe + "  ",
			want:  exe,
		},
		{
			name:  "unterminated quote falls back to the remainder",
			input: `"C:\Program Files\App\uninstall.exe`,
			want:  `C:\Program Files\App\uninstall.exe`,
		},
		{name: "empty", input: "", want: ""},
		{name: "whitespace only", input: "   ", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := uninstallerPath(tc.input); got != tc.want {
				t.Errorf("uninstallerPath(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// When no prefix of an unquoted string is a real file, the caller has to be
// given something it can act on. Returning the first token keeps the error
// message concrete instead of echoing the whole registry value back.
func TestUninstallerPathFallsBackWhenNothingExists(t *testing.T) {
	got := uninstallerPath(`/nonexistent/path/that/has spaces/uninstall.exe /X`)
	if got != "/nonexistent/path/that/has" {
		t.Errorf("uninstallerPath = %q, want the first token", got)
	}
	if _, err := resolveUninstallerPath(got); err == nil {
		t.Error("a non-existent fallback path must be rejected by resolveUninstallerPath")
	}
}

// resolveUninstallerPath is the trust boundary between a registry value and
// arbitrary code execution. A recorded UninstallString is a hint from a program
// that may since have been removed, so running whatever path it names without
// checking is executing an attacker-planted path.
func TestResolveUninstallerPathRefusesWhatItCannotVerify(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "uninstall.exe")
	if err := os.WriteFile(real, nil, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveUninstallerPath(real); err != nil {
		t.Errorf("a real file must be accepted, got %v", err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"relative path", "uninstall.exe"},
		{"dot relative path", "./uninstall.exe"},
		{"missing file", filepath.Join(dir, "nope.exe")},
		{"directory", dir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := resolveUninstallerPath(tc.path); err == nil {
				t.Errorf("resolveUninstallerPath(%q) = %q, want an error", tc.path, got)
			}
		})
	}
}
