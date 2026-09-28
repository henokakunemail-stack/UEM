package software

import (
	"strings"
	"unicode"
)

// normalizeProgramName reduces a program name to something comparable.
//
// Vendors do not agree on how to write a product name, and the registry, the
// package database and the console's package record each use a different
// spelling of the same thing. A survey of this machine's uninstall entries shows
// the pattern directly: "WinRAR 7.23 (64-bit)" in the registry, "winrar" in the
// catalog, "WinRAR archiver" as the registry key name. Comparing those verbatim
// finds nothing.
func normalizeProgramName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		case unicode.IsSpace(r) || r == '-' || r == '_' || r == '.' || r == ',' || r == '(' || r == ')':
			// Collapse runs of separators to a single space so "WinRAR (64-bit)"
			// and "winrar 64 bit" compare equal.
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
		default:
			// Punctuation that carries no meaning is dropped outright rather
			// than turned into a separator, so "7-zip" and "7zip" match.
		}
	}
	return strings.TrimSpace(b.String())
}

// nameMatches reports whether an installed program satisfies what the operator
// asked for.
//
// The rule is that the query must *lead* the candidate: the candidate's words
// must start with the query's words, in the same order. Everything the operator
// named has to be there, and the candidate may only add words after them --
// which is what a version suffix and a bit-width marker are.
//
//	"winrar"      -> "WinRAR 7.23 (64-bit)"      match (version follows the name)
//	"Adobe Acrobat" -> "Adobe Acrobat Reader DC" match
//	"7-Zip"       -> "7-Zip 23.01 (x64)"         match
//
// What it refuses is a query that matches somewhere in the middle of a longer
// name, because that is how the wrong program gets removed. The rule this
// replaced -- "every query word appears somewhere in the candidate" -- allowed
// all three of these, and each is a real hazard on a machine with one Adobe
// product or one archiver on it:
//
//	"Acrobat" -> "Adobe Acrobat Reader DC"   both of the two Adobe products match
//	"zip"     -> "7-Zip 23.01"               the operator meant WinZip
//	"Reader"  -> "Adobe Acrobat Reader DC"   a component word, not a product
//
// None of those is caught by the ambiguity check in Uninstall, which only
// refuses when two or more programs match. A single false match is the case
// that check cannot see, and it has no undo.
//
// A second, narrower allowance: a hyphen inside a word is a spelling habit, not
// a separate word, so "7zip" and "7-Zip" compare equal. That is the one vendor
// inconsistency worth absorbing, and it applies only to the whole-string
// comparison, never to the word sequence below.
// compact drops the spaces normalizeProgramName inserted for a hyphen, so
// "7-Zip" and "7zip" -- which normalise to "7 zip" and "7zip" -- compare equal.
// It only applies to the whole-string comparison above, never to the word
// sequence, because a hyphen between two words of a longer name is a real
// separator and absorbing it would make "Acrobat Reader" match "Reader DC".
func compact(s string) string {
	return strings.ReplaceAll(s, " ", "")
}

func nameMatches(query, candidate string) bool {
	q := normalizeProgramName(query)
	c := normalizeProgramName(candidate)
	if q == "" || c == "" {
		return false
	}
	if q == c || compact(q) == compact(c) {
		return true
	}

	qw := strings.Fields(q)
	cw := strings.Fields(c)
	if len(qw) == 0 || len(qw) > len(cw) {
		return false
	}
	for i, word := range qw {
		if cw[i] != word {
			return false
		}
	}
	return true
}
