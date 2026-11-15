package embed

import (
	"image"
	"image/draw"
	"math"
)

// ImageSize is the side of the square the vision tower reads.
const ImageSize = 224

// Pixels is the vision tower's input for one picture: squashed to
// ImageSize x ImageSize without keeping its proportions, which is how SigLIP2
// was trained, scaled to [-1, 1], one plane per channel.
//
// The resize is PIL's bilinear, to the bit, rather than x/image/draw's: PIL
// widens the filter by the reduction, so every source pixel counts, and it is
// what the model saw in training and what the spike measured. Being a level
// out in a few pixels was enough to move a vector to 0.995 of Python's, because
// the int8 vision tower magnifies what it is given.
func Pixels(src image.Image) []float32 {
	rgba := image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
	w, h := rgba.Bounds().Dx(), rgba.Bounds().Dy()

	// Horizontal first, rounded to bytes in between, as PIL does.
	kx := kernels(w, ImageSize)
	mid := make([]uint8, h*ImageSize*3)
	for y := range h {
		row := rgba.Pix[y*rgba.Stride:]
		for x, k := range kx {
			r, g, b := roundHalf, roundHalf, roundHalf
			for i, wt := range k.w {
				p := row[(k.from+i)*4:]
				r += wt * int64(p[0])
				g += wt * int64(p[1])
				b += wt * int64(p[2])
			}
			o := (y*ImageSize + x) * 3
			mid[o], mid[o+1], mid[o+2] = clip8(r), clip8(g), clip8(b)
		}
	}

	ky := kernels(h, ImageSize)
	out := make([]float32, 3*ImageSize*ImageSize)
	plane := ImageSize * ImageSize
	for y, k := range ky {
		for x := range ImageSize {
			c := [3]int64{roundHalf, roundHalf, roundHalf}
			for i, wt := range k.w {
				p := mid[((k.from+i)*ImageSize+x)*3:]
				c[0] += wt * int64(p[0])
				c[1] += wt * int64(p[1])
				c[2] += wt * int64(p[2])
			}
			for ch := range 3 {
				out[ch*plane+y*ImageSize+x] = float32(clip8(c[ch]))/127.5 - 1
			}
		}
	}
	return out
}

type kernel struct {
	from int
	w    []int64
}

// precisionBits is PIL's fixed point for 8-bit images, and roundHalf what it
// starts every sum at so the shift rounds rather than truncates.
const (
	precisionBits = 32 - 8 - 2
	roundHalf     = int64(1) << (precisionBits - 1)
)

// kernels is PIL's ImagingResample coefficient table for the triangle filter,
// in the same order of operations, so the doubles round as PIL's do before
// they become fixed point.
func kernels(in, out int) []kernel {
	scale := float64(in) / float64(out)
	filterScale := max(scale, 1)
	support := filterScale // the triangle's support is one
	ss := 1 / filterScale
	ks := make([]kernel, out)
	for o := range ks {
		center := (float64(o) + 0.5) * scale
		lo := max(int(center-support+0.5), 0)
		hi := min(int(center+support+0.5), in)
		pre := make([]float64, hi-lo)
		var sum float64
		for i := range pre {
			t := math.Abs((float64(i+lo) - center + 0.5) * ss)
			if t < 1 {
				pre[i] = 1 - t
			}
			sum += pre[i]
		}
		k := kernel{from: lo, w: make([]int64, len(pre))}
		for i, w := range pre {
			if sum != 0 {
				w /= sum
			}
			if w < 0 {
				k.w[i] = int64(-0.5 + w*(1<<precisionBits))
			} else {
				k.w[i] = int64(0.5 + w*(1<<precisionBits))
			}
		}
		ks[o] = k
	}
	return ks
}

func clip8(v int64) uint8 {
	return uint8(min(max(v>>precisionBits, 0), 255))
}
