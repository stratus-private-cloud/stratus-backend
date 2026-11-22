package media

import (
	"sync"
	"time"
)

// Deferred is when each file that failed a background pass may be tried
// again, for the three passes that walk a library newest first (#557).
//
// A failure that is not a verdict on the file -- a store that did not answer, a
// write it refused -- leaves the row as it was, so the next query offers the
// same file at the head of the queue again: once a second for the pictures,
// which re-read and re-decoded them each time, and once an hour for the other
// two, with everything older stuck behind. Remembered here rather than on the
// row because there is one process: a restart costs each one retry, and no
// engine needs a column for it.
type Deferred struct {
	mu    sync.Mutex
	until map[int64]deferral
}

type deferral struct {
	at   time.Time
	wait time.Duration
}

// The first wait, and the most it doubles to.
const (
	firstDeferral = time.Minute
	lastDeferral  = 24 * time.Hour
)

// NewDeferred is an empty one.
func NewDeferred() *Deferred { return &Deferred{until: map[int64]deferral{}} }

// Len is how many files are waiting, which a pass adds to the rows it asks
// for so that skipping them still leaves it a batch.
func (d *Deferred) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.until)
}

// Ready is whether id may be tried at now.
func (d *Deferred) Ready(id int64, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.until[id]
	return !ok || !now.Before(w.at)
}

// Failed puts id off, twice as long as the last time.
func (d *Deferred) Failed(id int64, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	wait := min(max(2*d.until[id].wait, firstDeferral), lastDeferral)
	d.until[id] = deferral{at: now.Add(wait), wait: wait}
}

// Done forgets id, which got a picture or a verdict.
func (d *Deferred) Done(id int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.until, id)
}
