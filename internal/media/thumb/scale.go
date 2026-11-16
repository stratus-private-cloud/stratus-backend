package thumb

import (
	"image"
	"math"
)

// Scale fits the sr part of src into the whole of dst with x/image's
// Catmull-Rom, as draw.CatmullRom.Scale(dst, dst.Bounds(), src, sr,
// draw.Src, nil) does and to the same bit, but a source row at a time
// (#530).
//
// x/image runs the horizontal pass over the whole picture before the
// vertical one, into float64s: destination width times source height times
// 32 bytes, which is 154 MB to fit a 12 MP photograph in 1,600 pixels. Each
// destination row needs only the few source rows under its kernel, and those
// come in order, so here they are kept in a ring that holds that many and
// nothing else -- half a megabyte for the same photograph. The arithmetic is
// copied from x/image, conversions and all: an explicit float64() is what
// stops the compiler fusing a multiply and an add on arm64, and the test
// compares the two pixel for pixel.
func Scale(dst *image.RGBA, src image.Image, sr image.Rectangle) {
	dr := dst.Bounds()
	dw, dh := dr.Dx(), dr.Dy()
	sw, sh := sr.Dx(), sr.Dy()
	if dw <= 0 || dh <= 0 || sw <= 0 || sh <= 0 {
		return
	}
	across, down := distribute(dw, sw), distribute(dh, sh)
	row := rowReader(src, sr, across)

	span := 1
	for _, s := range down.sources {
		if s.j > s.i {
			span = max(span, down.taps[s.j-1].coord-down.taps[s.i].coord+1)
		}
	}
	ring := make([][][4]float64, span)
	held := make([]int, span)
	for i := range ring {
		ring[i] = make([][4]float64, dw)
		held[i] = -1
	}

	for dy, s := range down.sources {
		taps := down.taps[s.i:s.j]
		for _, c := range taps {
			slot := c.coord % span
			if held[slot] != c.coord {
				row(c.coord, ring[slot])
				held[slot] = c.coord
			}
		}
		d := dst.PixOffset(dr.Min.X, dr.Min.Y+dy)
		for dx := range dw {
			var pr, pg, pb, pa float64
			for _, c := range taps {
				p := &ring[c.coord%span][dx]
				pr += float64(p[0] * c.weight)
				pg += float64(p[1] * c.weight)
				pb += float64(p[2] * c.weight)
				pa += float64(p[3] * c.weight)
			}
			pr, pg, pb = min(pr, pa), min(pg, pa), min(pb, pa)
			dst.Pix[d+0] = uint8(ftou(pr*s.invTotalWeight) >> 8)
			dst.Pix[d+1] = uint8(ftou(pg*s.invTotalWeight) >> 8)
			dst.Pix[d+2] = uint8(ftou(pb*s.invTotalWeight) >> 8)
			dst.Pix[d+3] = uint8(ftou(pa*s.invTotalWeight) >> 8)
			d += 4
		}
	}
}

// rowReader is the horizontal pass over one source row, by the type of the
// picture as x/image chooses: the fast paths only for a rectangle inside the
// picture, since they index the pixels without a bounds check.
func rowReader(src image.Image, sr image.Rectangle, across distribution) func(y int, out [][4]float64) {
	if !sr.In(src.Bounds()) {
		return generic(src, sr, across)
	}
	switch src := src.(type) {
	case *image.YCbCr:
		switch src.SubsampleRatio {
		case image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422,
			image.YCbCrSubsampleRatio420, image.YCbCrSubsampleRatio440:
			return ycbcr(src, sr, across)
		}
		return generic(src, sr, across)
	case *image.RGBA:
		return rgba(src, sr, across)
	case *image.NRGBA:
		return nrgba(src, sr, across)
	case *image.Gray:
		return gray(src, sr, across)
	case image.RGBA64Image:
		return rgba64(src, sr, across)
	}
	return generic(src, sr, across)
}

func ycbcr(src *image.YCbCr, sr image.Rectangle, across distribution) func(int, [][4]float64) {
	halfX := src.SubsampleRatio == image.YCbCrSubsampleRatio422 || src.SubsampleRatio == image.YCbCrSubsampleRatio420
	halfY := src.SubsampleRatio == image.YCbCrSubsampleRatio420 || src.SubsampleRatio == image.YCbCrSubsampleRatio440
	// Every row reads the same columns, so where they are is worked out once.
	luma, chroma := make([]int, len(across.taps)), make([]int, len(across.taps))
	for k, c := range across.taps {
		x := sr.Min.X + c.coord
		luma[k] = x - src.Rect.Min.X
		chroma[k] = x - src.Rect.Min.X
		if halfX {
			chroma[k] = x/2 - src.Rect.Min.X/2
		}
	}
	return func(y int, out [][4]float64) {
		sy := sr.Min.Y + y
		yRow := (sy - src.Rect.Min.Y) * src.YStride
		cRow := (sy - src.Rect.Min.Y) * src.CStride
		if halfY {
			cRow = (sy/2 - src.Rect.Min.Y/2) * src.CStride
		}
		for t, s := range across.sources {
			var pr, pg, pb float64
			for k := s.i; k < s.j; k++ {
				pi, pj := yRow+luma[k], cRow+chroma[k]
				// image/color's YCbCr.RGBA, inline, as x/image has it.
				pyy1 := int(src.Y[pi]) * 0x10101
				pcb1 := int(src.Cb[pj]) - 128
				pcr1 := int(src.Cr[pj]) - 128
				pru := clamp16((pyy1 + 91881*pcr1) >> 8)
				pgu := clamp16((pyy1 - 22554*pcb1 - 46802*pcr1) >> 8)
				pbu := clamp16((pyy1 + 116130*pcb1) >> 8)
				w := across.taps[k].weight
				pr += float64(float64(pru) * w)
				pg += float64(float64(pgu) * w)
				pb += float64(float64(pbu) * w)
			}
			out[t] = [4]float64{pr * s.invTotalWeightFFFF, pg * s.invTotalWeightFFFF, pb * s.invTotalWeightFFFF, 1}
		}
	}
}

func rgba(src *image.RGBA, sr image.Rectangle, across distribution) func(int, [][4]float64) {
	cols := columns(across, sr.Min.X-src.Rect.Min.X, 4)
	return func(y int, out [][4]float64) {
		base := (sr.Min.Y + y - src.Rect.Min.Y) * src.Stride
		for t, s := range across.sources {
			var pr, pg, pb, pa float64
			for k := s.i; k < s.j; k++ {
				pi := base + cols[k]
				w := across.taps[k].weight
				pr += float64(float64(uint32(src.Pix[pi+0])*0x101) * w)
				pg += float64(float64(uint32(src.Pix[pi+1])*0x101) * w)
				pb += float64(float64(uint32(src.Pix[pi+2])*0x101) * w)
				pa += float64(float64(uint32(src.Pix[pi+3])*0x101) * w)
			}
			out[t] = [4]float64{pr * s.invTotalWeightFFFF, pg * s.invTotalWeightFFFF, pb * s.invTotalWeightFFFF, pa * s.invTotalWeightFFFF}
		}
	}
}

func nrgba(src *image.NRGBA, sr image.Rectangle, across distribution) func(int, [][4]float64) {
	cols := columns(across, sr.Min.X-src.Rect.Min.X, 4)
	return func(y int, out [][4]float64) {
		base := (sr.Min.Y + y - src.Rect.Min.Y) * src.Stride
		for t, s := range across.sources {
			var pr, pg, pb, pa float64
			for k := s.i; k < s.j; k++ {
				pi := base + cols[k]
				w := across.taps[k].weight
				pau := uint32(src.Pix[pi+3]) * 0x101
				pr += float64(float64(uint32(src.Pix[pi+0])*pau/0xff) * w)
				pg += float64(float64(uint32(src.Pix[pi+1])*pau/0xff) * w)
				pb += float64(float64(uint32(src.Pix[pi+2])*pau/0xff) * w)
				pa += float64(float64(pau) * w)
			}
			out[t] = [4]float64{pr * s.invTotalWeightFFFF, pg * s.invTotalWeightFFFF, pb * s.invTotalWeightFFFF, pa * s.invTotalWeightFFFF}
		}
	}
}

func gray(src *image.Gray, sr image.Rectangle, across distribution) func(int, [][4]float64) {
	cols := columns(across, sr.Min.X-src.Rect.Min.X, 1)
	return func(y int, out [][4]float64) {
		base := (sr.Min.Y + y - src.Rect.Min.Y) * src.Stride
		for t, s := range across.sources {
			var pr float64
			for k := s.i; k < s.j; k++ {
				pr += float64(float64(uint32(src.Pix[base+cols[k]])*0x101) * across.taps[k].weight)
			}
			pr *= s.invTotalWeightFFFF
			out[t] = [4]float64{pr, pr, pr, 1}
		}
	}
}

func rgba64(src image.RGBA64Image, sr image.Rectangle, across distribution) func(int, [][4]float64) {
	return func(y int, out [][4]float64) {
		for t, s := range across.sources {
			var pr, pg, pb, pa float64
			for _, c := range across.taps[s.i:s.j] {
				pu := src.RGBA64At(sr.Min.X+c.coord, sr.Min.Y+y)
				pr += float64(float64(pu.R) * c.weight)
				pg += float64(float64(pu.G) * c.weight)
				pb += float64(float64(pu.B) * c.weight)
				pa += float64(float64(pu.A) * c.weight)
			}
			out[t] = [4]float64{pr * s.invTotalWeightFFFF, pg * s.invTotalWeightFFFF, pb * s.invTotalWeightFFFF, pa * s.invTotalWeightFFFF}
		}
	}
}

func generic(src image.Image, sr image.Rectangle, across distribution) func(int, [][4]float64) {
	return func(y int, out [][4]float64) {
		for t, s := range across.sources {
			var pr, pg, pb, pa float64
			for _, c := range across.taps[s.i:s.j] {
				pru, pgu, pbu, pau := src.At(sr.Min.X+c.coord, sr.Min.Y+y).RGBA()
				pr += float64(float64(pru) * c.weight)
				pg += float64(float64(pgu) * c.weight)
				pb += float64(float64(pbu) * c.weight)
				pa += float64(float64(pau) * c.weight)
			}
			out[t] = [4]float64{pr * s.invTotalWeightFFFF, pg * s.invTotalWeightFFFF, pb * s.invTotalWeightFFFF, pa * s.invTotalWeightFFFF}
		}
	}
}

// columns is where each tap's pixel starts in a row of size-byte pixels.
func columns(across distribution, offset, size int) []int {
	out := make([]int, len(across.taps))
	for k, c := range across.taps {
		out[k] = (offset + c.coord) * size
	}
	return out
}

func clamp16(v int) int { return min(max(v, 0), 0xffff) }

// distribution is x/image's distrib: for each destination column or row, the
// source ones it is made of and their weights.
type distribution struct {
	sources []span
	taps    []tap
}

type span struct {
	i, j               int
	invTotalWeight     float64
	invTotalWeightFFFF float64
}

type tap struct {
	coord  int
	weight float64
}

// distribute is x/image's newDistrib for its CatmullRom.
func distribute(dw, sw int) distribution {
	const support = 2
	scale := float64(sw) / float64(dw)
	halfWidth, argScale := float64(support), 1.0
	if scale > 1 {
		halfWidth *= scale
		argScale = 1 / scale
	}
	sources := make([]span, dw)
	var taps []tap
	for x := range sources {
		center := float64((float64(x)+0.5)*scale) - 0.5
		i := max(int(math.Floor(center-halfWidth)), 0)
		j := min(int(math.Ceil(center+halfWidth)), sw)
		first, total := len(taps), 0.0
		for coord := i; coord < j; coord++ {
			t := math.Abs((center - float64(coord)) * argScale)
			if t >= support {
				continue
			}
			w := catmullRom(t)
			if w == 0 {
				continue
			}
			total += w
			taps = append(taps, tap{coord, w})
		}
		total = 1 / total
		sources[x] = span{i: first, j: len(taps), invTotalWeight: total, invTotalWeightFFFF: total / 0xffff}
	}
	return distribution{sources, taps}
}

func catmullRom(t float64) float64 {
	if t < 1 {
		return float64((float64(1.5*t)-2.5)*t*t) + 1
	}
	return float64((float64(float64(float64(-0.5*t)+2.5)*t)-4)*t) + 2
}

// ftou is x/image's: [0, 1] to [0, 0xffff], rounded.
func ftou(f float64) uint16 {
	i := int32(float64(0xffff*f) + 0.5)
	if i > 0xffff {
		return 0xffff
	}
	if i > 0 {
		return uint16(i)
	}
	return 0
}
