package faces

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"testing"
)

// around is a face near one of a few people, or a stranger near nobody.
func around(r *rand.Rand, centres [][]float32) []float32 {
	v := make([]float32, Dimensions)
	if c := r.IntN(len(centres) + 2); c < len(centres) {
		copy(v, centres[c])
		for i := range v {
			v[i] += float32(r.NormFloat64()) * 0.06
		}
	} else {
		for i := range v {
			v[i] = float32(r.NormFloat64())
		}
	}
	normalize(v)
	return v
}

// canon is a grouping in a form two groupings can be compared in.
func canon(g grouping) string {
	var joins []string
	for p, ids := range g.joins {
		ids = slices.Clone(ids)
		slices.Sort(ids)
		joins = append(joins, fmt.Sprint(p, ids))
	}
	sort.Strings(joins)
	var seeds []string
	for _, ids := range g.seeds {
		ids = slices.Clone(ids)
		slices.Sort(ids)
		seeds = append(seeds, fmt.Sprint(ids))
	}
	sort.Strings(seeds)
	return fmt.Sprint(joins, seeds)
}

// TestARoundRememberingWhatItComparedGroupsAsComparingEverything is #560's
// promise: comparing only what is newly loose gives exactly the groups that
// comparing every pair does, round after round, with faces arriving, joining
// people and being taken out of them in between.
func TestARoundRememberingWhatItComparedGroupsAsComparingEverything(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 560))
	centres := make([][]float32, 6)
	for i := range centres {
		centres[i] = around(r, nil)
	}
	l := newLinks()
	var all []face
	next, person := int64(1), int64(1000)
	for round := range 12 {
		for range 40 {
			all = append(all, face{id: next, v: around(r, centres)})
			next++
		}
		want := canon(group(all))
		g, err := groupWith(t.Context(), all, l)
		if err != nil {
			t.Fatal(err)
		}
		if got := canon(g); got != want {
			t.Fatalf("round %d: remembering gave %s, comparing everything %s", round, got, want)
		}

		// What the round decided is written, as Group writes it.
		for p, ids := range g.joins {
			for i := range all {
				if slices.Contains(ids, all[i].id) {
					all[i].person = p
				}
			}
		}
		for _, ids := range g.seeds {
			person++
			for i := range all {
				if slices.Contains(ids, all[i].id) {
					all[i].person = person
				}
			}
		}
		// And somebody takes a face or two out of whoever it was under.
		for range 2 {
			i := r.IntN(len(all))
			if all[i].person != 0 {
				all[i].not = append(all[i].not, all[i].person)
				all[i].person = 0
			}
		}
	}
}

// TestARoundStopsWhenAskedTo: shutting down does not wait for a round, and a
// round cut short teaches the next one nothing.
func TestARoundStopsWhenAskedTo(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var all []face
	for i := range 1000 {
		all = append(all, face{id: int64(i + 1), v: around(r, nil)})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	l := newLinks()
	if _, err := groupWith(ctx, all, l); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled round = %v", err)
	}
	if len(l.loose) != 0 || len(l.pairs) != 0 {
		t.Error("a round cut short remembered something")
	}
}

// BenchmarkARoundAfterTenThousandLooseFaces is the cost #560 was about: a
// round of 256 new faces against ten thousand loose ones, which compared every
// pair of the ten thousand again before.
func BenchmarkARoundAfterTenThousandLooseFaces(b *testing.B) {
	r := rand.New(rand.NewPCG(3, 4))
	var all []face
	for i := range 10_000 {
		all = append(all, face{id: int64(i + 1), v: around(r, nil)})
	}
	l := newLinks()
	if _, err := groupWith(context.Background(), all, l); err != nil {
		b.Fatal(err)
	}
	for i := range 256 {
		all = append(all, face{id: int64(20_000 + i), v: around(r, nil)})
	}
	for b.Loop() {
		snapshot := &links{loose: maps(l.loose), pairs: maps(l.pairs)}
		if _, err := groupWith(context.Background(), all, snapshot); err != nil {
			b.Fatal(err)
		}
	}
}

func maps[K comparable](m map[K]bool) map[K]bool {
	out := make(map[K]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// BenchmarkARoundComparingEverything is the same round as it was before, for
// the number beside the one above.
func BenchmarkARoundComparingEverything(b *testing.B) {
	r := rand.New(rand.NewPCG(3, 4))
	var all []face
	for i := range 10_256 {
		all = append(all, face{id: int64(i + 1), v: around(r, nil)})
	}
	for b.Loop() {
		group(all)
	}
}
