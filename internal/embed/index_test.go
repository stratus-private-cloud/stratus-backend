package embed_test

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
)

func unit(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var n float64
	for i := range v {
		v[i] = float32(r.NormFloat64())
		n += float64(v[i]) * float64(v[i])
	}
	for i := range v {
		v[i] /= float32(math.Sqrt(n))
	}
	return v
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func TestAStoredVectorScoresAsTheFloatsDo(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	x := embed.NewIndex(768)
	q := unit(r, 768)
	for id := range int64(50) {
		v := unit(r, 768)
		if err := x.Put(id, embed.Quantize(v)); err != nil {
			t.Fatal(err)
		}
		hits := x.Search(q, -1, 1000)
		for _, h := range hits {
			if h.FileID == id {
				if d := math.Abs(float64(h.Score - dot(q, v))); d > 0.01 {
					t.Errorf("file %d scores %.4f, the floats %.4f", id, h.Score, dot(q, v))
				}
			}
		}
	}
}

func TestSearchIsClosestFirstAboveTheFloor(t *testing.T) {
	x := embed.NewIndex(2)
	for id, v := range map[int64][]float32{1: {1, 0}, 2: {0.6, 0.8}, 3: {0, 1}, 4: {-1, 0}} {
		if err := x.Put(id, embed.Quantize(v)); err != nil {
			t.Fatal(err)
		}
	}
	hits := x.Search([]float32{1, 0}, 0.5, 10)
	if len(hits) != 2 || hits[0].FileID != 1 || hits[1].FileID != 2 {
		t.Fatalf("Search = %+v, want 1 then 2", hits)
	}
	if hits := x.Search([]float32{1, 0}, -2, 3); len(hits) != 3 {
		t.Errorf("Search with a limit of 3 = %d hits", len(hits))
	}
}

func TestPutReplacesAndRemoveForgets(t *testing.T) {
	x := embed.NewIndex(2)
	for id, v := range map[int64][]float32{1: {1, 0}, 2: {0, 1}, 3: {-1, 0}} {
		if err := x.Put(id, embed.Quantize(v)); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.Put(3, embed.Quantize([]float32{1, 0})); err != nil {
		t.Fatal(err)
	}
	x.Remove(1)
	x.Remove(99)
	if x.Len() != 2 {
		t.Fatalf("Len = %d, want 2", x.Len())
	}
	hits := x.Search([]float32{1, 0}, 0.5, 10)
	if len(hits) != 1 || hits[0].FileID != 3 {
		t.Errorf("Search = %+v, want the replaced vector alone", hits)
	}
	if hits := x.Search([]float32{0, 1}, 0.5, 10); len(hits) != 1 || hits[0].FileID != 2 {
		t.Errorf("Search = %+v, want the vector moved into the removed one's place", hits)
	}
}

func TestAVectorOfAnotherSizeIsRefused(t *testing.T) {
	x := embed.NewIndex(768)
	if err := x.Put(1, embed.Quantize(make([]float32, 512))); !errors.Is(err, embed.ErrDimension) {
		t.Errorf("Put = %v, want ErrDimension", err)
	}
}

func BenchmarkSearch50k(b *testing.B) {
	r := rand.New(rand.NewPCG(3, 4))
	x := embed.NewIndex(768)
	for id := range int64(50_000) {
		_ = x.Put(id, embed.Quantize(unit(r, 768)))
	}
	q := unit(r, 768)
	b.ResetTimer()
	for range b.N {
		x.Search(q, 0.05, 50)
	}
}
