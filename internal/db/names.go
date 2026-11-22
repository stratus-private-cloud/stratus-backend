package db

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// CompareNames orders two names the way a person reads a list of them (#592):
// case and accents ignored, so Álvaro is with the A's and not after Zoe, as a
// byte comparison put him. Names equal under that order fall back to their
// bytes, so the order is total. Ñ is filed with N: a Spanish collation would
// put it after, at the price of a collation table in the binary.
func CompareNames(a, b string) int {
	if c := strings.Compare(FoldName(a), FoldName(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// FoldName is a name lower-cased and without the marks its letters carry once
// decomposed, so é is e. What CompareNames compares, and what a list grouped by
// initial should take the initial of, or Á is a heading of its own.
func FoldName(name string) string {
	var folded strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(name)) {
		if !unicode.Is(unicode.Mn, r) {
			folded.WriteRune(r)
		}
	}
	return folded.String()
}
