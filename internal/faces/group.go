package faces

import (
	"context"
	"math"
	"slices"
)

// JoinFloor is the least similarity to somebody's average face a loose face
// joins them at, and SeedFloor the least two loose faces are linked at to
// start somebody new, with MinGroup the fewest faces that is.
//
// Measured on LFW with this pipeline (#519): 6,711 faces of 610 people and
// 1,496 strangers, grouped as Group does it. Different people reach 0.5 in
// one pair of thirty thousand; the same person stays above 0.57 in nineteen
// pairs of twenty. At these floors 7 of 604 groups mix two people, a
// stranger is put with somebody once in 250, and somebody is one group and
// not two. Against the nearest face rather than an average, 0.45 and 0.5
// mixed 61 groups where the average mixed 35: one look-alike is enough to
// link two people.
// Strict on purpose: merging two groups and taking a stranger out of one are
// both forms on a page, but a stranger also drags the average they are in.
const (
	JoinFloor = 0.5
	SeedFloor = 0.55
	MinGroup  = 3
)

// face is one face as grouping sees it, and not is who it was taken out of.
type face struct {
	id, person int64
	v          []float32
	not        []int64
}

// grouping is what one round decided: loose faces joining somebody who
// exists, and groups of loose faces that are somebody new.
type grouping struct {
	joins map[int64][]int64
	seeds [][]int64
}

// links is what the rounds before this one learned about the loose faces
// (#560): which faces were loose at the end of the last round, and every pair
// of loose faces ever found alike enough to link. A face's vector never
// changes, so a pair compared once is compared for good, and a round only has
// to compare what has become loose since -- a new face, or one taken out of
// somebody -- against everything loose. That every pair of faces loose now has
// been compared holds after each round by the same argument, so the groups are
// exactly the ones comparing all of them would make.
type links struct {
	loose map[int64]bool
	pairs map[[2]int64]bool
}

func newLinks() *links {
	return &links{loose: map[int64]bool{}, pairs: map[[2]int64]bool{}}
}

// checkEvery is how many faces go by between two looks at the context: often
// enough that shutting down does not wait for a round, rarely enough to cost
// nothing.
const checkEvery = 256

// group decides where the loose faces go, comparing every pair of them. It is
// grouping with nothing remembered, which is what a test asks for.
func group(all []face) grouping {
	g, _ := groupWith(context.Background(), all, newLinks())
	return g
}

// groupWith decides where the loose faces go. Faces somebody already has are
// only read, for the average each person is compared by: a face put somewhere
// by hand is never moved. What it learns goes into l only when it finishes, so
// a round cut short leaves the next one where this one started.
func groupWith(ctx context.Context, all []face, l *links) (grouping, error) {
	sums := map[int64][]float32{}
	for _, f := range all {
		if f.person == 0 {
			continue
		}
		s := sums[f.person]
		if s == nil {
			s = make([]float32, len(f.v))
			sums[f.person] = s
		}
		for i, x := range f.v {
			s[i] += x
		}
	}
	people := make([]int64, 0, len(sums))
	for p, s := range sums {
		normalize(s)
		people = append(people, p)
	}

	g := grouping{joins: map[int64][]int64{}}
	var loose []face
	for n, f := range all {
		if n%checkEvery == 0 && ctx.Err() != nil {
			return grouping{}, ctx.Err()
		}
		if f.person != 0 {
			continue
		}
		best, at := float32(JoinFloor), int64(0)
		for _, p := range people {
			if slices.Contains(f.not, p) {
				continue
			}
			if s := dot(f.v, sums[p]); s >= best {
				best, at = s, p
			}
		}
		if at != 0 {
			g.joins[at] = append(g.joins[at], f.id)
			continue
		}
		loose = append(loose, f)
	}

	// What is new to being loose is compared with everything loose; the rest
	// was compared in a round before. Two new ones are compared once.
	at := make(map[int64]int, len(loose))
	for i, f := range loose {
		at[f.id] = i
	}
	found := map[[2]int64]bool{}
	for i, f := range loose {
		if i%checkEvery == 0 && ctx.Err() != nil {
			return grouping{}, ctx.Err()
		}
		if l.loose[f.id] {
			continue
		}
		for j, o := range loose {
			if j == i || (!l.loose[o.id] && j < i) {
				continue
			}
			if dot(f.v, o.v) >= SeedFloor {
				found[pairOf(f.id, o.id)] = true
			}
		}
	}

	parent := make([]int, len(loose))
	for i := range parent {
		parent[i] = i
	}
	root := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	link := func(pair [2]int64) {
		a, aok := at[pair[0]]
		b, bok := at[pair[1]]
		if aok && bok {
			parent[root(a)] = root(b)
		}
	}
	for pair := range l.pairs {
		link(pair)
	}
	for pair := range found {
		link(pair)
	}
	members := map[int][]int64{}
	var order []int
	for i, f := range loose {
		r := root(i)
		if members[r] == nil {
			order = append(order, r)
		}
		members[r] = append(members[r], f.id)
	}
	for _, r := range order {
		if len(members[r]) >= MinGroup {
			g.seeds = append(g.seeds, members[r])
		}
	}

	// Kept: the pairs between faces that still exist, and who is loose.
	exists := make(map[int64]bool, len(all))
	for _, f := range all {
		exists[f.id] = true
	}
	for pair := range l.pairs {
		if !exists[pair[0]] || !exists[pair[1]] {
			delete(l.pairs, pair)
		}
	}
	for pair := range found {
		l.pairs[pair] = true
	}
	clear(l.loose)
	for _, f := range loose {
		l.loose[f.id] = true
	}
	return g, nil
}

func pairOf(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func normalize(v []float32) {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return
	}
	k := float32(1 / math.Sqrt(n))
	for i := range v {
		v[i] *= k
	}
}
