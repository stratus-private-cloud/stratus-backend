package embed_test

import (
	"image/png"
	"math"
	"os"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
)

// TestPixelsAreWhatPILGives compares the resize with PIL's own on a picture
// with gradients, hard edges and a block of colour, which it has to match
// exactly.
func TestPixelsAreWhatPILGives(t *testing.T) {
	f, err := os.Open("testdata/pixels_src.png")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	src, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/pixels_pil.bin")
	if err != nil {
		t.Fatal(err)
	}

	got := embed.Pixels(src)
	if len(got) != len(want) {
		t.Fatalf("got %d values, want %d", len(got), len(want))
	}
	off := 0
	for i, v := range got {
		if math.Abs(float64(v)-(float64(want[i])/127.5-1)) > 1e-6 {
			off++
		}
	}
	if off > 0 {
		t.Errorf("%d of %d values differ from PIL's", off, len(got))
	}
}
