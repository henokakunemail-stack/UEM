//go:build !windows

package software

import "strings"

// splitArgs parses an operator-supplied argument string into argv.
//
// Unix packages take their switches the way a shell splits them: whitespace
// separates, and a double-quoted run stays one argument so that
// `-o target_dir=/opt/My App` survives. dpkg, rpm and /usr/sbin/installer all
// receive argv directly, so this only has to match shell word-splitting, not a
// full shell grammar — no expansion, no globbing, no variable substitution,
// because a package argument is data, not a script to be evaluated.
//
// ponytail: backslash escapes and single quotes are not honoured. No installer
// switch on Linux or macOS needs them, and every extra rule is another way for
// an operator's carefully quoted path to be re-split. Add them if a package
// ever ships a switch that requires them.
func splitArgs(s string) []string {
	if s == "" {
		return nil
	}
	var (
		out   []string
		cur   strings.Builder
		quote bool
		open  bool
	)
	for _, r := range s {
		switch {
		case r == '"':
			quote = !quote
			open = true
		case !quote && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			if open {
				out = append(out, cur.String())
				cur.Reset()
				open = false
			}
		default:
			cur.WriteRune(r)
			open = true
		}
	}
	if open {
		out = append(out, cur.String())
	}
	return out
}
