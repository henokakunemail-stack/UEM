//go:build windows

package software

import (
	"golang.org/x/sys/windows"
)

// splitArgs parses an operator-supplied argument string into argv.
//
// strings.Fields is wrong here, and wrong in the way that silently installs to
// the wrong place: `/DIR="C:\Program Files\App"` becomes two arguments,
// `/DIR="C:\Program` and `Files\App"`, so the installer either lands somewhere
// the operator did not ask for or fails outright.
//
// windows.DecomposeCommandLine looks like the right tool and is not, which is
// worth writing down because the mistake is easy to repeat. It is a thin
// wrapper over CommandLineToArgvW, and CommandLineToArgvW follows one rule
// this function has no way to express: argv[0] is the program's own path. The
// first whitespace-delimited token is therefore *always* consumed as the
// program name and never returned as an argument. Feeding it a bare argument
// string silently drops the first argument:
//
//	DecomposeCommandLine(`/DIR="C:\Program Files\App"`)
//	  -> ["/DIR=\"C:\Program", "Files\\App"]     wrong, and the quotes survive
//
// Verified on this machine against a real spawned program, which receives one
// correct argument when Go passes it as a separate argv entry:
//
//	cmd /c `probe.exe /DIR="C:\Program Files\App"`  ->  3 argv, quotes intact
//
// Prefixing a synthetic program name and dropping argv[0] afterwards is what
// makes the API mean what we need:
//
//	DecomposeCommandLine(`prog.exe /DIR="C:\Program Files\App"`)[1:]
//	  -> ["/DIR=C:\Program Files\App"]             correct
func splitArgs(s string) []string {
	if s == "" {
		return nil
	}
	// The prefix has to be a bare token, not a quoted one: a quoted prefix
	// absorbs the leading quote of an argument like `"C:\Program Files\x"`,
	// merging the two. Verified -- with the prefix quoted, /s parses to nothing
	// at all, because the whole string collapses into the argv[0] slot.
	prefixed, err := windows.DecomposeCommandLine("prog.exe " + s)
	if err != nil {
		// A NUL, or a line too long for CommandLineToArgvW. Refusing beats
		// shipping a mangled argv to an installer: a half-parsed switch list
		// installs something nobody asked for.
		return nil
	}
	// DecomposeCommandLine always returns the program name, except for an empty
	// input, which is handled above.
	if len(prefixed) == 0 {
		return nil
	}
	return prefixed[1:]
}
