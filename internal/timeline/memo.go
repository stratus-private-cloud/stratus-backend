package timeline

import (
	"context"
	"sync"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// memoFor is how long what was read is answered from memory.
//
// A Tree lives for one request, so every step of the viewer, every page of a
// grid and every GET of a photograph by its address read its month whole again
// -- a month of three thousand photographs, forty columns each, to name one
// (#561). Ten seconds, as the people listing is kept: long enough that a
// session of clicking reads a month once, short enough that a photograph just
// uploaded is in the grid by the time anybody looks for it.
const memoFor = 10 * time.Second

// memoLimit is how many answers are kept before the oldest are dropped, which
// is a month per page somebody has open and then some.
const memoLimit = 256

// Memoize is source with its answers kept for a few seconds, for the two
// adapters that build a Tree per request. One for the process, shared by the
// pages and the WebDAV mount, since they read the same months.
//
// Changed throws everything away, and the indexer's loop calls it whenever it
// writes a row: what a tree by date shows is the index, so a photograph is in
// it the moment it is read rather than up to ten seconds later.
func Memoize(source Source) *Memo {
	return &Memo{source: source, kept: map[memoKey]memoEntry{}, clock: time.Now}
}

// Memo is a Source that remembers; see Memoize.
type Memo struct {
	source Source
	mu     sync.Mutex
	kept   map[memoKey]memoEntry
	// era moves on every Changed, so an answer read before one is not kept
	// after it.
	era   uint64
	clock func() time.Time
}

// Changed forgets everything kept.
func (m *Memo) Changed() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.kept)
	m.era++
}

func (m *Memo) now() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.era
}

type memoKey struct {
	owner string
	kind  db.Kind
	f     db.CaptureFilter
	all   bool
}

type memoEntry struct {
	at       time.Time
	months   []db.Month
	captures []db.Capture
}

func (m *Memo) get(k memoKey) (memoEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.kept[k]
	if !ok || m.clock().Sub(e.at) > memoFor {
		return memoEntry{}, false
	}
	return e, true
}

func (m *Memo) put(k memoKey, e memoEntry, era uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if era != m.era {
		return
	}
	if len(m.kept) >= memoLimit {
		for key, old := range m.kept {
			if m.clock().Sub(old.at) > memoFor {
				delete(m.kept, key)
			}
		}
		if len(m.kept) >= memoLimit {
			clear(m.kept)
		}
	}
	e.at = m.clock()
	m.kept[k] = e
}

// Months implements Source.
func (m *Memo) Months(ctx context.Context, owner string, kind db.Kind) ([]db.Month, error) {
	k := memoKey{owner: owner, kind: kind, all: true}
	if e, ok := m.get(k); ok {
		return e.months, nil
	}
	era := m.now()
	months, err := m.source.Months(ctx, owner, kind)
	if err != nil {
		return nil, err
	}
	m.put(k, memoEntry{months: months}, era)
	return months, nil
}

// Timeline implements Source. A page is kept by the filter that asked for it,
// which is the month and the cursor inside it.
func (m *Memo) Timeline(ctx context.Context, owner string, f db.CaptureFilter) ([]db.Capture, error) {
	k := memoKey{owner: owner, kind: f.Kind, f: f}
	if e, ok := m.get(k); ok {
		return e.captures, nil
	}
	era := m.now()
	page, err := m.source.Timeline(ctx, owner, f)
	if err != nil {
		return nil, err
	}
	m.put(k, memoEntry{captures: page}, era)
	return page, nil
}
