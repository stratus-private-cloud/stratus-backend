package storage_test

import (
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/storage"
)

// TestForward is the header a range is asked for by when the size is not
// needed to ask (#562), and the ranges that cannot be.
func TestForward(t *testing.T) {
	t.Parallel()
	for rng, want := range map[storage.Range]string{
		storage.From(0):       "bytes=0-",
		storage.From(7):       "bytes=7-",
		storage.Slice(3, 4):   "bytes=3-6",
		storage.All():         "",
		storage.Suffix(5):     "",
		storage.Slice(3, 0):   "",
		storage.Slice(-1, 10): "",
	} {
		if got, ok := rng.Forward(); got != want || ok != (want != "") {
			t.Errorf("Forward(%+v) = %q, %v; want %q", rng, got, ok, want)
		}
	}
}
