package app

import (
	"math"
	"runtime/debug"
	"testing"
)

// TestTheHeapIsHeldUnderTheMachinesMemory is the ceiling a photograph's
// scratch needs on a small machine, and a GOMEMLIMIT somebody set is left as
// it is.
func TestTheHeapIsHeldUnderTheMachinesMemory(t *testing.T) {
	was := debug.SetMemoryLimit(math.MaxInt64)
	t.Cleanup(func() { debug.SetMemoryLimit(was) })

	holdTheHeap(512 << 20)
	if got := debug.SetMemoryLimit(-1); got != 384<<20 {
		t.Errorf("the heap is held under %d, want three quarters of 512 MB", got)
	}
	holdTheHeap(1 << 30)
	if got := debug.SetMemoryLimit(-1); got != 384<<20 {
		t.Errorf("a limit already set became %d", got)
	}

	debug.SetMemoryLimit(math.MaxInt64)
	holdTheHeap(0)
	if got := debug.SetMemoryLimit(-1); got != math.MaxInt64 {
		t.Errorf("a machine that does not say was given a limit of %d", got)
	}
}
