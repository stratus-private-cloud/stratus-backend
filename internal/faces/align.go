package faces

import (
	"image"
	"image/draw"
	"math"

	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
)

// template is where SFace expects the five landmarks in its 112 square:
// OpenCV's FaceRecognizerSF.alignCrop, which is what it was trained on.
var template = [5][2]float64{
	{38.2946, 51.6963}, {73.5318, 51.5014}, {56.0252, 71.7366}, {41.5493, 92.3655}, {70.7299, 92.2041},
}

// similarity is the rotation, uniform scale and shift taking from onto to
// with the least squared error: Umeyama's, in two dimensions, where it has a
// closed form. It maps p to (a*x - b*y + tx, b*x + a*y + ty).
type similarity struct{ a, b, tx, ty float64 }

func fit(from, to [5][2]float64) similarity {
	var mfx, mfy, mtx, mty float64
	for i := range from {
		mfx += from[i][0] / 5
		mfy += from[i][1] / 5
		mtx += to[i][0] / 5
		mty += to[i][1] / 5
	}
	var sxx, sxy, norm float64
	for i := range from {
		fx, fy := from[i][0]-mfx, from[i][1]-mfy
		tx, ty := to[i][0]-mtx, to[i][1]-mty
		sxx += fx*tx + fy*ty
		sxy += fx*ty - fy*tx
		norm += fx*fx + fy*fy
	}
	if norm == 0 {
		return similarity{a: 1, tx: mtx - mfx, ty: mty - mfy}
	}
	a, b := sxx/norm, sxy/norm
	return similarity{a: a, b: b, tx: mtx - (a*mfx - b*mfy), ty: mty - (b*mfx + a*mfy)}
}

func (s similarity) scale() float64 { return math.Hypot(s.a, s.b) }

// inverse maps a point of the destination back into the source.
func (s similarity) inverse(x, y float64) (float64, float64) {
	d := s.a*s.a + s.b*s.b
	x, y = x-s.tx, y-s.ty
	return (s.a*x + s.b*y) / d, (-s.b*x + s.a*y) / d
}

// alignInput is the face at landmarks in picture, turned and scaled onto
// SFace's template: one plane per channel, red first, 0 to 255.
//
// The warp samples bilinearly, which reads four pixels for each it writes, so
// a face more than twice the template's size is reduced first: a portrait
// whose face fills a phone's picture would otherwise be read off a sparse
// grid of its pixels.
func alignInput(picture image.Image, landmarks [5][2]float64) []float32 {
	src := toRGBA(picture)
	t := fit(landmarks, template)
	if k := t.scale(); k < 0.5 {
		var cut image.Rectangle
		src, cut = reduceAround(src, t, 2*k)
		for i := range landmarks {
			landmarks[i] = [2]float64{(landmarks[i][0] - float64(cut.Min.X)) * 2 * k, (landmarks[i][1] - float64(cut.Min.Y)) * 2 * k}
		}
		t = fit(landmarks, template)
	}

	side := embed.FaceSize
	plane := side * side
	px := make([]float32, 3*plane)
	for y := range side {
		for x := range side {
			sx, sy := t.inverse(float64(x), float64(y))
			r, g, b := bilinear(src, sx, sy)
			i := y*side + x
			px[i], px[plane+i], px[2*plane+i] = r, g, b
		}
	}
	return px
}

// reduceAround is the part of src the template's square maps back onto,
// scaled by k, and where in src that part was.
func reduceAround(src *image.RGBA, t similarity, k float64) (*image.RGBA, image.Rectangle) {
	side := float64(embed.FaceSize)
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{0, 0}, {side, 0}, {0, side}, {side, side}} {
		x, y := t.inverse(c[0], c[1])
		minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
	}
	cut := image.Rect(int(math.Floor(minX)), int(math.Floor(minY)), int(math.Ceil(maxX)), int(math.Ceil(maxY)))
	cut = cut.Intersect(src.Bounds())
	if cut.Empty() {
		return src, src.Bounds()
	}
	w := max(1, int(math.Round(float64(cut.Dx())*k)))
	h := max(1, int(math.Round(float64(cut.Dy())*k)))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	thumb.Scale(dst, src, cut)
	return dst, cut
}

// bilinear samples src at (x, y), with what lies outside it black.
func bilinear(src *image.RGBA, x, y float64) (float32, float32, float32) {
	b := src.Bounds()
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := x-x0, y-y0
	var r, g, bl float64
	for _, c := range [4]struct {
		dx, dy int
		w      float64
	}{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
		px, py := int(x0)+c.dx+b.Min.X, int(y0)+c.dy+b.Min.Y
		if c.w == 0 || !(image.Point{X: px, Y: py}).In(b) {
			continue
		}
		p := src.Pix[src.PixOffset(px, py):]
		r += c.w * float64(p[0])
		g += c.w * float64(p[1])
		bl += c.w * float64(p[2])
	}
	return float32(r), float32(g), float32(bl)
}

func toRGBA(picture image.Image) *image.RGBA {
	if rgba, ok := picture.(*image.RGBA); ok && rgba.Bounds().Min == (image.Point{}) {
		return rgba
	}
	b := picture.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), picture, b.Min, draw.Src)
	return rgba
}
