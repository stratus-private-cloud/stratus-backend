package faces

import (
	"errors"
	"image"
	"image/color"
	"math"
	"strings"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
)

// yunetOutputs is YuNet's twelve outputs with nothing found, and set puts a
// face in the anchor at row r, column c of a stride: its centre offset by
// half a cell and its box 4 cells wide, so it is easy to say where it went.
type yunetOutputs [][]float32

func emptyOutputs() yunetOutputs {
	outs := make(yunetOutputs, 12)
	for k, s := range strides {
		n := (embed.DetectSize / s) * (embed.DetectSize / s)
		outs[k], outs[3+k], outs[6+k], outs[9+k] = make([]float32, n), make([]float32, n), make([]float32, 4*n), make([]float32, 10*n)
	}
	return outs
}

func (o yunetOutputs) set(k, r, c int, score float32) {
	cols := embed.DetectSize / strides[k]
	i := r*cols + c
	o[k][i], o[3+k][i] = score, score
	copy(o[6+k][4*i:], []float32{0.5, 0.5, float32(math.Log(4)), float32(math.Log(4))})
	for n := range 5 {
		o[9+k][10*i+2*n], o[9+k][10*i+2*n+1] = float32(n)*0.1, float32(n)*0.1
	}
}

func TestYuNetsOutputsAreReadIntoFacesInThePicturesPixels(t *testing.T) {
	outs := emptyOutputs()
	outs.set(1, 10, 20, 0.95) // stride 16
	outs.set(1, 10, 21, 0.97) // the same face again, a little better
	outs.set(0, 2, 2, 0.5)    // not sure enough

	// A picture twice YuNet's square: everything comes back doubled.
	got := decode(outs, 0.5)
	if len(got) != 1 {
		t.Fatalf("decode = %+v, want one face", got)
	}
	d := got[0]
	wantX := ((21+0.5)*16 - 32) / 0.5
	if math.Abs(d.X-wantX) > 1e-6 || math.Abs(d.W-128) > 1e-6 || math.Abs(d.Score-0.97) > 1e-6 {
		t.Errorf("decode = %+v, want x %.1f, width 128, score 0.97", d, wantX)
	}
	if lm := d.Landmarks[4]; math.Abs(lm[0]-(0.4+21)*16/0.5) > 1e-6 || math.Abs(lm[1]-(0.4+10)*16/0.5) > 1e-6 {
		t.Errorf("the last landmark is %v", lm)
	}

	if got := decode(outs[:11], 1); got != nil {
		t.Errorf("decode of eleven outputs = %v, want nothing", got)
	}
	outs[0] = outs[0][:10]
	if got := decode(outs, 1); got != nil {
		t.Errorf("decode of a short output = %v, want nothing", got)
	}
}

func TestAPictureIsFittedInYuNetsSquareBlueFirst(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1280, 640))
	for y := range 640 {
		for x := range 1280 {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	px, scale := detectInput(img)
	if scale != 0.5 || len(px) != 3*embed.DetectSize*embed.DetectSize {
		t.Fatalf("detectInput = %d values at %v", len(px), scale)
	}
	plane := embed.DetectSize * embed.DetectSize
	if px[0] != 50 || px[plane] != 100 || px[2*plane] != 200 {
		t.Errorf("the first pixel is %v %v %v, want blue, green, red", px[0], px[plane], px[2*plane])
	}
	// The bottom half is the padding.
	if last := px[plane-1]; last != 0 {
		t.Errorf("the last pixel is %v, want the padding's 0", last)
	}

	// A small picture is enlarged to fill it.
	if _, scale := detectInput(image.NewRGBA(image.Rect(0, 0, 320, 160))); scale != 2 {
		t.Errorf("a picture of 320 is scaled by %v, want 2", scale)
	}
}

func TestASimilarityIsFoundFromItsOwnImage(t *testing.T) {
	want := similarity{a: 0.8 * math.Cos(0.3), b: 0.8 * math.Sin(0.3), tx: 12, ty: -7}
	var from [5][2]float64
	for i, p := range template {
		from[i] = [2]float64{p[0] * 3, p[1]*2 + 5}
	}
	var to [5][2]float64
	for i, p := range from {
		to[i] = [2]float64{want.a*p[0] - want.b*p[1] + want.tx, want.b*p[0] + want.a*p[1] + want.ty}
	}
	got := fit(from, to)
	if math.Abs(got.a-want.a) > 0.05 || math.Abs(got.b-want.b) > 0.05 {
		t.Errorf("fit = %+v, want close to %+v", got, want)
	}
	x, y := got.inverse(to[0][0], to[0][1])
	if math.Hypot(x-from[0][0], y-from[0][1]) > 6 {
		t.Errorf("inverse(%v) = %v %v, want near %v", to[0], x, y, from[0])
	}
	if s := fit([5][2]float64{}, template); s.a != 1 || s.b != 0 {
		t.Errorf("fit from one point = %+v, want a shift", s)
	}
}

// drawnFace draws a picture whose left half is red and right half blue, with the
// landmarks where the template has them, scaled by k.
func drawnFace(k float64) (image.Image, [5][2]float64) {
	side := int(112 * k)
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := range side {
		for x := range side {
			c := color.RGBA{R: 255, A: 255}
			if x >= side/2 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	var lm [5][2]float64
	for i, p := range template {
		lm[i] = [2]float64{p[0] * k, p[1] * k}
	}
	return img, lm
}

func TestAFaceIsAlignedOntoTheTemplateRedFirst(t *testing.T) {
	for _, k := range []float64{1, 5} { // as it is, and reduced first
		img, lm := drawnFace(k)
		px := alignInput(img, lm)
		plane := embed.FaceSize * embed.FaceSize
		left, right := 50*embed.FaceSize+10, 50*embed.FaceSize+100
		if px[left] < 250 || px[2*plane+left] > 5 || px[right] > 5 || px[2*plane+right] < 250 {
			t.Errorf("at %v: left %v/%v, right %v/%v; want red on the left and blue on the right",
				k, px[left], px[2*plane+left], px[right], px[2*plane+right])
		}
	}
}

func TestACropIsASquareOfTheFace(t *testing.T) {
	img, _ := drawnFace(4)
	crop, err := cropOf(img, Detection{X: 100, Y: 100, W: 200, H: 260})
	if err != nil {
		t.Fatal(err)
	}
	if len(crop) == 0 || crop[0] != 0xff || crop[1] != 0xd8 {
		t.Errorf("the crop is not a JPEG")
	}
	if _, err := cropOf(img, Detection{X: 5000, Y: 5000, W: 10, H: 10}); err != nil {
		t.Errorf("a box off the picture: %v", err)
	}
}

func unit(axis int, wobble float32) []float32 {
	v := make([]float32, Dimensions)
	v[axis] = 1
	v[(axis+1)%Dimensions] = wobble
	normalize(v)
	return v
}

func TestGroupingJoinsSeedsAndLeavesAlone(t *testing.T) {
	all := []face{
		{id: 1, person: 100, v: unit(0, 0)},
		{id: 2, person: 100, v: unit(0, 0.1)},
		{id: 3, v: unit(0, 0.2)},            // joins 100
		{id: 4, person: 200, v: unit(0, 0)}, // put under 200 by hand: stays
		{id: 5, v: unit(5, 0)},
		{id: 6, v: unit(5, 0.1)},
		{id: 7, v: unit(5, 0.2)}, // 5, 6 and 7 are somebody new
		{id: 8, v: unit(9, 0)},
		{id: 9, v: unit(9, 0.1)}, // two are not enough
		{id: 10, v: unit(20, 0)},
	}
	g := group(all)
	if got := g.joins[100]; len(got) != 1 || got[0] != 3 {
		t.Errorf("joins to 100 = %v, want [3]", got)
	}
	if len(g.joins) != 1 {
		t.Errorf("joins = %v, want only 100's", g.joins)
	}
	if len(g.seeds) != 1 || len(g.seeds[0]) != 3 || g.seeds[0][0] != 5 {
		t.Errorf("seeds = %v, want [[5 6 7]]", g.seeds)
	}
}

func TestAFaceTakenOutOfSomebodyGoesToTheNextBest(t *testing.T) {
	all := []face{
		{id: 1, person: 100, v: unit(0, 0)},
		{id: 2, person: 200, v: unit(0, 0.3)},
		{id: 3, v: unit(0, 0.05), not: []int64{100}}, // nearest 100, but not them
		{id: 4, v: unit(0, 0.05), not: []int64{100, 200}},
	}
	g := group(all)
	if got := g.joins[200]; len(got) != 1 || got[0] != 3 {
		t.Errorf("joins to 200 = %v, want [3]", got)
	}
	if len(g.joins[100]) != 0 {
		t.Errorf("joins to 100 = %v, want none", g.joins[100])
	}
}

func TestNamesAreOneSegmentEach(t *testing.T) {
	people := []db.Person{{ID: 1, Name: "Ana"}, {ID: 2, Name: "ana"}, {ID: 3, Name: "Bea"}, {ID: 4}}
	names := Names(people)
	want := map[int64]string{1: "Ana (1)", 2: "ana (2)", 3: "Bea", 4: "Person 4"}
	for id, n := range want {
		if names[id] != n {
			t.Errorf("Names[%d] = %q, want %q", id, names[id], n)
		}
	}
	if p, err := Resolve(people, "Bea"); err != nil || p.ID != 3 {
		t.Errorf("Resolve(Bea) = %+v, %v", p, err)
	}
	if _, err := Resolve(people, "Ana"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Resolve(Ana) = %v, want ErrNotFound: two people have it", err)
	}

	for in, want := range map[string]string{"  Ana   María ": "Ana María", "": ""} {
		if got, err := CleanName(in); err != nil || got != want {
			t.Errorf("CleanName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"a/b", `a\b`, "..", strings.Repeat("x", MaxName+1)} {
		if _, err := CleanName(bad); err == nil {
			t.Errorf("CleanName(%q) took it", bad)
		}
	}
}
