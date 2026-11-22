package embed

import (
	"cmp"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"sync"
)

// A stored vector is its scale as a little-endian float32 and then one signed
// byte per dimension: a quarter of the floats, and the spike measured no
// difference in what a search finds (0.791 against 0.790).
const scaleBytes = 4

// Quantize writes a vector down as it is stored.
func Quantize(v []float32) []byte {
	var peak float32
	for _, x := range v {
		peak = max(peak, float32(math.Abs(float64(x))))
	}
	scale := peak / 127
	if scale == 0 {
		scale = 1
	}
	out := make([]byte, scaleBytes+len(v))
	binary.LittleEndian.PutUint32(out, math.Float32bits(scale))
	for i, x := range v {
		out[scaleBytes+i] = byte(int8(math.Round(float64(x / scale)))) //nolint:gosec // the bits of an int8, kept as one
	}
	return out
}

// Dequantize is a stored vector as floats, or ErrDimension for one that is
// not dim long.
func Dequantize(stored []byte, dim int) ([]float32, error) {
	if len(stored) != scaleBytes+dim {
		return nil, ErrDimension
	}
	scale := math.Float32frombits(binary.LittleEndian.Uint32(stored))
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(int8(stored[scaleBytes+i])) * scale //nolint:gosec // the bits of an int8, kept as one
	}
	return v, nil
}

// Hit is one photograph a query found, and how close it is: a cosine, since
// both vectors have unit length.
type Hit struct {
	FileID int64
	Score  float32
}

// Index is every photograph's vector in memory, searched by brute force.
//
// No approximate index: a personal library is tens of thousands of vectors,
// which is milliseconds to compare against a query exhaustively, and an
// exhaustive search cannot miss what an approximate one would.
type Index struct {
	mu     sync.RWMutex
	dim    int
	pos    map[int64]int
	ids    []int64
	scales []float32
	values []int8
}

// ErrDimension is a stored vector that is not the size of the others, which
// is a bundle and a table that disagree.
var ErrDimension = errors.New("embed: a vector of another dimension")

// NewIndex is an empty index for vectors of dim dimensions, with room for
// size of them before it grows: built by append, the last growth held the
// old array and one twice its size at once, about 150 MB at a hundred
// thousand photographs (#566).
func NewIndex(dim, size int) *Index {
	size = max(size, 0)
	return &Index{
		dim: dim, pos: make(map[int64]int, size),
		ids: make([]int64, 0, size), scales: make([]float32, 0, size), values: make([]int8, 0, size*dim),
	}
}

// Put adds a stored vector, or replaces the one the file had.
func (x *Index) Put(fileID int64, stored []byte) error {
	if len(stored) != scaleBytes+x.dim {
		return ErrDimension
	}
	scale := math.Float32frombits(binary.LittleEndian.Uint32(stored))
	x.mu.Lock()
	defer x.mu.Unlock()
	i, ok := x.pos[fileID]
	if !ok {
		i = len(x.ids)
		x.pos[fileID] = i
		x.ids = append(x.ids, fileID)
		x.scales = append(x.scales, 0)
		x.values = append(x.values, make([]int8, x.dim)...)
	}
	x.scales[i] = scale
	for d, b := range stored[scaleBytes:] {
		x.values[i*x.dim+d] = int8(b) //nolint:gosec // a byte written as an int8, read back as one
	}
	return nil
}

// Remove forgets a file's vector, moving the last one into its place.
func (x *Index) Remove(fileID int64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	i, ok := x.pos[fileID]
	if !ok {
		return
	}
	last := len(x.ids) - 1
	if i != last {
		x.ids[i], x.scales[i] = x.ids[last], x.scales[last]
		copy(x.values[i*x.dim:(i+1)*x.dim], x.values[last*x.dim:])
		x.pos[x.ids[i]] = i
	}
	delete(x.pos, fileID)
	x.ids, x.scales, x.values = x.ids[:last], x.scales[:last], x.values[:last*x.dim]
}

// Vector is a file's stored vector as floats, and false when it has none.
func (x *Index) Vector(fileID int64) ([]float32, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	i, ok := x.pos[fileID]
	if !ok {
		return nil, false
	}
	v := make([]float32, x.dim)
	for d, b := range x.values[i*x.dim : (i+1)*x.dim] {
		v[d] = float32(b) * x.scales[i]
	}
	return v, true
}

// Len is how many vectors it holds.
func (x *Index) Len() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.ids)
}

// Search returns the limit closest photographs scoring at least floor,
// closest first.
func (x *Index) Search(query []float32, floor float32, limit int) []Hit {
	x.mu.RLock()
	defer x.mu.RUnlock()
	if len(query) != x.dim || limit <= 0 {
		return nil
	}
	var hits []Hit
	for i, id := range x.ids {
		row := x.values[i*x.dim : (i+1)*x.dim]
		var dot float32
		for d, v := range row {
			dot += query[d] * float32(v)
		}
		if score := dot * x.scales[i]; score >= floor {
			hits = append(hits, Hit{FileID: id, Score: score})
		}
	}
	slices.SortFunc(hits, func(a, b Hit) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.FileID, b.FileID)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
