package web

import "testing"

// TestAnAccentedNameIsFiledUnderItsLetter: with the list ordered ignoring
// accents (#592), Álvaro sits among the A's, so Á as a heading of its own
// would split them.
func TestAnAccentedNameIsFiledUnderItsLetter(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{"Álvaro": "A", "óscar": "O", "Zoe": "Z", "42": "#"} {
		if got := initial(name); got != want {
			t.Errorf("initial(%q) = %q, want %q", name, got, want)
		}
	}
}
