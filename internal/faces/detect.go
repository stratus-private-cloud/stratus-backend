package faces

import (
	"image"
	"image/draw"
	"math"
	"slices"

	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
)

// Detection is one face YuNet found: its box, its five landmarks -- the eyes,
// the nose and the corners of the mouth, the subject's right first -- and how
// sure it is, all in the pixels of the picture it was found in.
type Detection struct {
	X, Y, W, H float64
	Landmarks  [5][2]float64
	Score      float64
}

// Confidence is the least score a face is kept with, and overlap the IoU
// above which two boxes are one face: OpenCV's own defaults for this model.
const (
	Confidence = 0.9
	overlap    = 0.3
)

// strides are YuNet's three heads, and the order its outputs come in: cls,
// obj, bbox and kps, each at every stride.
var strides = []int{8, 16, 32}

// detectInput is picture fitted in YuNet's square from the top left corner,
// one plane per channel, blue first, 0 to 255, and the factor the picture was
// scaled by to fit.
func detectInput(picture image.Image) ([]float32, float64) {
	b := picture.Bounds()
	side := embed.DetectSize
	// Enlarged as well as reduced: a small picture's faces are small, and
	// YuNet finds a face it can see better than one it has to squint at.
	scale := float64(side) / float64(max(b.Dx(), b.Dy()))
	w, h := max(1, int(math.Round(float64(b.Dx())*scale))), max(1, int(math.Round(float64(b.Dy())*scale)))

	fitted := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() && h == b.Dy() {
		draw.Draw(fitted, fitted.Bounds(), picture, b.Min, draw.Src)
	} else {
		thumb.Scale(fitted, picture, b)
	}
	plane := side * side
	px := make([]float32, 3*plane)
	for y := range h {
		row := fitted.Pix[y*fitted.Stride:]
		for x := range w {
			p := row[4*x:]
			i := y*side + x
			px[i], px[plane+i], px[2*plane+i] = float32(p[2]), float32(p[1]), float32(p[0])
		}
	}
	return px, scale
}

// decode turns YuNet's twelve outputs into the faces it is sure of, in the
// pixels of the picture detectInput was given, best first.
func decode(outs [][]float32, scale float64) []Detection {
	if len(outs) != 4*len(strides) {
		return nil
	}
	var found []Detection
	for k, stride := range strides {
		cls, obj, bbox, kps := outs[k], outs[len(strides)+k], outs[2*len(strides)+k], outs[3*len(strides)+k]
		cols := embed.DetectSize / stride
		if len(cls) != cols*cols || len(obj) != len(cls) || len(bbox) != 4*len(cls) || len(kps) != 10*len(cls) {
			return nil
		}
		s := float64(stride)
		for i := range cls {
			score := math.Sqrt(clamp01(float64(cls[i])) * clamp01(float64(obj[i])))
			if score < Confidence {
				continue
			}
			r, c := float64(i/cols), float64(i%cols)
			b := bbox[4*i:]
			cx, cy := (c+float64(b[0]))*s, (r+float64(b[1]))*s
			w, h := math.Exp(float64(b[2]))*s, math.Exp(float64(b[3]))*s
			d := Detection{X: (cx - w/2) / scale, Y: (cy - h/2) / scale, W: w / scale, H: h / scale, Score: score}
			for n := range d.Landmarks {
				d.Landmarks[n] = [2]float64{
					(float64(kps[10*i+2*n]) + c) * s / scale,
					(float64(kps[10*i+2*n+1]) + r) * s / scale,
				}
			}
			found = append(found, d)
		}
	}
	slices.SortStableFunc(found, func(a, b Detection) int { return cmpDesc(a.Score, b.Score) })
	var kept []Detection
	for _, d := range found {
		if !slices.ContainsFunc(kept, func(k Detection) bool { return iou(d, k) > overlap }) {
			kept = append(kept, d)
		}
	}
	return kept
}

func clamp01(v float64) float64 { return min(1, max(0, v)) }

func cmpDesc(a, b float64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

func iou(a, b Detection) float64 {
	w := min(a.X+a.W, b.X+b.W) - max(a.X, b.X)
	h := min(a.Y+a.H, b.Y+b.H) - max(a.Y, b.Y)
	if w <= 0 || h <= 0 {
		return 0
	}
	i := w * h
	return i / (a.W*a.H + b.W*b.H - i)
}
