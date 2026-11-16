package thumb

import (
	"bytes"
	"fmt"
	"image"
	"image/color/palette"
	"math/rand/v2"
	"runtime"
	"testing"

	"golang.org/x/image/draw"
)

// TestScaleIsXImagesCatmullRom is #530's promise: what Scale draws is what
// x/image's Catmull-Rom draws, to the bit, for every kind of picture a decode
// hands back, smaller and larger, whole and in part -- so that no thumbnail,
// vector or face read before it changes.
func TestScaleIsXImagesCatmullRom(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(5, 30))
	pictures := map[string]image.Image{}
	for _, ratio := range []image.YCbCrSubsampleRatio{
		image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420,
		image.YCbCrSubsampleRatio440, image.YCbCrSubsampleRatio411,
	} {
		// An odd corner, which is where the chroma's halving goes wrong.
		p := image.NewYCbCr(image.Rect(3, 5, 3+157, 5+123), ratio)
		fill(rng, p.Y, p.Cb, p.Cr)
		pictures[fmt.Sprintf("ycbcr %v", ratio)] = p
	}
	rgba := image.NewRGBA(image.Rect(-7, 2, 150, 120))
	fill(rng, rgba.Pix)
	for i := 3; i < len(rgba.Pix); i += 4 {
		// Premultiplied, so a channel above its alpha is not a colour.
		rgba.Pix[i-1], rgba.Pix[i-2], rgba.Pix[i-3] = min(rgba.Pix[i-1], rgba.Pix[i]),
			min(rgba.Pix[i-2], rgba.Pix[i]), min(rgba.Pix[i-3], rgba.Pix[i])
	}
	pictures["rgba"] = rgba
	nrgba := image.NewNRGBA(image.Rect(0, 0, 141, 99))
	fill(rng, nrgba.Pix)
	pictures["nrgba"] = nrgba
	grey := image.NewGray(image.Rect(1, 1, 130, 170))
	fill(rng, grey.Pix)
	pictures["gray"] = grey
	wide := image.NewRGBA64(image.Rect(0, 0, 120, 80))
	fill(rng, wide.Pix)
	pictures["rgba64"] = wide
	paletted := image.NewPaletted(image.Rect(0, 0, 90, 110), palette.Plan9)
	fill(rng, paletted.Pix)
	pictures["paletted"] = paletted
	pictures["cmyk"] = cmykOf(rng, 100, 70)

	sizes := []image.Point{{40, 30}, {17, 61}, {157, 123}, {300, 200}, {1, 1}, {640, 5}}
	for name, src := range pictures {
		b := src.Bounds()
		parts := map[string]image.Rectangle{
			"whole":     b,
			"inside":    image.Rect(b.Min.X+9, b.Min.Y+4, b.Max.X-13, b.Max.Y-21),
			"overhangs": image.Rect(b.Min.X-10, b.Min.Y+7, b.Max.X-5, b.Max.Y+12),
		}
		for part, sr := range parts {
			for _, size := range sizes {
				want := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
				draw.CatmullRom.Scale(want, want.Bounds(), src, sr, draw.Src, nil)
				got := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
				Scale(got, src, sr)
				if !bytes.Equal(got.Pix, want.Pix) {
					t.Errorf("%s, %s, into %v: not x/image's picture", name, part, size)
				}
			}
		}
	}

	// And into a destination whose corner is not the origin.
	src := pictures[fmt.Sprintf("ycbcr %v", image.YCbCrSubsampleRatio420)]
	want := image.NewRGBA(image.Rect(10, 20, 60, 50))
	draw.CatmullRom.Scale(want, want.Bounds(), src, src.Bounds(), draw.Src, nil)
	got := image.NewRGBA(image.Rect(10, 20, 60, 50))
	Scale(got, src, src.Bounds())
	if !bytes.Equal(got.Pix, want.Pix) {
		t.Error("into a destination off the origin: not x/image's picture")
	}
}

// TestScaleHoldsAFewRowsAndNoMore is the memory #530 was about: fitting a
// 12 MP photograph in 1,600 pixels took 154 MB of x/image's scratch.
func TestScaleHoldsAFewRowsAndNoMore(t *testing.T) {
	src := image.NewYCbCr(image.Rect(0, 0, 4000, 3000), image.YCbCrSubsampleRatio420)
	dst := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	Scale(dst, src, src.Bounds())
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 4<<20 {
		t.Errorf("Scale allocated %d MB, want a few rows' worth", got>>20)
	}
}

func fill(rng *rand.Rand, planes ...[]uint8) {
	for _, p := range planes {
		for i := range p {
			p[i] = uint8(rng.UintN(256))
		}
	}
}

func cmykOf(rng *rand.Rand, w, h int) image.Image {
	p := image.NewCMYK(image.Rect(0, 0, w, h))
	fill(rng, p.Pix)
	return p
}
