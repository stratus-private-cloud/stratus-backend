package embed

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// ErrFetching is the model still on its way, which a pass waits out rather
// than reports.
var ErrFetching = errors.New("embed: the model is being fetched")

// fetchRetry is how long a failed fetch waits before the next call tries
// again: an hour, since what fails a download -- no route out, a full disk --
// does not change in a minute.
const fetchRetry = time.Hour

// Lazy is the model, opened on first use and fetched first if it has to be.
//
// The fetch runs on its own, and every call meanwhile answers ErrFetching:
// 347 MB is minutes, and the loop that asks is the indexer's, which has
// uploads to see to in the meantime.
type Lazy struct {
	dir, helper string
	// bundle is what to fetch into dir, and nil for a directory somebody put
	// there themselves, which is opened and never written.
	bundle *Bundle
	client *http.Client

	mu       sync.Mutex
	model    *Model
	fetching bool
	failed   error
	failedAt time.Time
}

// NewLazy is the model in dir, fetched from bundle when dir does not hold it.
func NewLazy(dir, helper string, bundle *Bundle, client *http.Client) *Lazy {
	return &Lazy{dir: dir, helper: helper, bundle: bundle, client: client}
}

// Image is Model.Image, once there is a model.
func (l *Lazy) Image(ctx context.Context, img image.Image) ([]float32, error) {
	m, err := l.get()
	if err != nil {
		return nil, err
	}
	return m.Image(ctx, img)
}

// Text is Model.Text, once there is a model.
func (l *Lazy) Text(ctx context.Context, query string) ([]float32, error) {
	m, err := l.get()
	if err != nil {
		return nil, err
	}
	return m.Text(ctx, query)
}

// State is what a page says about the model: nothing once it is open, and
// otherwise why it is not.
func (l *Lazy) State() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.model != nil:
		return ""
	case l.fetching:
		return "fetching the model"
	case l.failed != nil:
		return l.failed.Error()
	}
	return "the model has not been needed yet"
}

func (l *Lazy) get() (*Model, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.model != nil {
		return l.model, nil
	}
	if l.fetching {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, ErrFetching)
	}
	m, err := Open(l.dir, l.helper)
	if err == nil {
		l.model, l.failed = m, nil
		return m, nil
	}
	if l.bundle == nil {
		l.failed = err
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if l.failed != nil && time.Since(l.failedAt) < fetchRetry {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, l.failed)
	}

	l.fetching = true
	go l.fetch()
	return nil, fmt.Errorf("%w: %w", ErrUnavailable, ErrFetching)
}

//nolint:contextcheck // detached on purpose: the call that started it is long gone
func (l *Lazy) fetch() {
	slog.Info("fetching the model", "name", l.bundle.Name, "bytes", l.bundle.Size, "from", l.bundle.URL)
	err := Fetch(context.Background(), l.client, *l.bundle, l.dir)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.fetching = false
	if err != nil {
		slog.Error("fetching the model", "err", err)
		l.failed, l.failedAt = err, time.Now()
		return
	}
	slog.Info("fetched the model", "name", l.bundle.Name, "dir", l.dir)
	l.failed = nil
}

// Close stops the model's processes, if it was ever opened.
func (l *Lazy) Close() {
	l.mu.Lock()
	m := l.model
	l.mu.Unlock()
	if m != nil {
		m.Close()
	}
}
