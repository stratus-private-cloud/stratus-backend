package web

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"golang.org/x/image/font/sfnt"
)

// TestTheSymbolsFontIsNamedByItsBytes: the name is the only thing that changes
// the font's URL, under a cache header that says it never changes. A new
// subset copied over the old name would be an old font in every browser that
// had seen it, for a year.
func TestTheSymbolsFontIsNamedByItsBytes(t *testing.T) {
	t.Parallel()
	body, err := staticFS.ReadFile("static/stratus/" + symbolsFont)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if want := "symbols-" + hex.EncodeToString(sum[:])[:8] + ".ttf"; symbolsFont != want {
		t.Errorf("the font is called %s and its bytes say %s", symbolsFont, want)
	}
	css, err := staticFS.ReadFile("static/stratus/stratus.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "url("+symbolsFont+")") {
		t.Errorf("stratus.css does not ask for %s", symbolsFont)
	}
}

// TestEveryMarkIsInTheFont: the subset is stratus-app's, cut to the names that
// app draws, so a codepoint written here that it does not have is a box on the
// page and nothing else says so.
func TestEveryMarkIsInTheFont(t *testing.T) {
	t.Parallel()
	body, err := staticFS.ReadFile("static/stratus/" + symbolsFont)
	if err != nil {
		t.Fatal(err)
	}
	font, err := sfnt.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	// The templates' own: search, share, rename, delete, details, a row's menu.
	actions := []string{"\uef7a", "\ue80d", "\uf097", "\ue92e", "\ue88e", "\ue5d4"}
	var buf sfnt.Buffer
	for _, mark := range append([]string{
		symFolder, symFilm, symTrack, symPDF, symArchive,
		symSheet, symEvent, symContact, symDocument,
	}, actions...) {
		r := []rune(mark)[0]
		if i, gerr := font.GlyphIndex(&buf, r); gerr != nil || i == 0 {
			t.Errorf("U+%04X is not in the font", r)
		}
	}
}
