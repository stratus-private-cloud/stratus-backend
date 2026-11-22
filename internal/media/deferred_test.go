package media_test

import (
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/media"
)

// TestDeferredDoublesAndForgets: a minute, then two, up to a day; and a file
// that got somewhere is tried at once next time.
func TestDeferredDoublesAndForgets(t *testing.T) {
	t.Parallel()
	d := media.NewDeferred()
	now := time.Now()
	if !d.Ready(1, now) {
		t.Fatal("a file nothing failed on is not ready")
	}
	d.Failed(1, now)
	if d.Ready(1, now.Add(59*time.Second)) || !d.Ready(1, now.Add(time.Minute)) {
		t.Error("the first wait is not a minute")
	}
	d.Failed(1, now)
	if d.Ready(1, now.Add(119*time.Second)) || !d.Ready(1, now.Add(2*time.Minute)) {
		t.Error("the second wait is not two minutes")
	}
	for range 20 {
		d.Failed(1, now)
	}
	if !d.Ready(1, now.Add(24*time.Hour)) {
		t.Error("the wait grew past a day")
	}
	if d.Len() != 1 {
		t.Errorf("Len = %d, want 1", d.Len())
	}
	d.Done(1)
	if !d.Ready(1, now) || d.Len() != 0 {
		t.Error("a file done with is still put off")
	}
}
