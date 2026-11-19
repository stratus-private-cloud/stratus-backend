package faces

import (
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

// group decides where the loose faces go. Faces somebody already has are only
// read, for the average each person is compared by: a face put somewhere by
// hand is never moved.
func group(all []face) grouping {
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
	for _, f := range all {
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

	// What is left is linked pair by pair, and a group is what hangs together.
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
	for i := range loose {
		for j := i + 1; j < len(loose); j++ {
			if dot(loose[i].v, loose[j].v) >= SeedFloor {
				parent[root(i)] = root(j)
			}
		}
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
	return g
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
