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

// loader is a bundle's models, opened on first use and fetched first if they
// have to be.
//
// The fetch runs on its own, and every call meanwhile answers ErrFetching:
// hundreds of megabytes are minutes, and the loop that asks is the indexer's,
// which has uploads to see to in the meantime.
type loader[M interface{ Close() }] struct {
	dir, helper string
	// bundle is what to fetch into dir, and nil for a directory somebody put
	// there themselves, which is opened and never written.
	bundle *Bundle
	client *http.Client
	open   func(dir, helper string) (M, error)

	mu       sync.Mutex
	model    M
	opened   bool
	fetching bool
	failed   error
	failedAt time.Time
}

// State is what a page says about the model: nothing once it is open, and
// otherwise why it is not.
func (l *loader[M]) State() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.opened:
		return ""
	case l.fetching:
		return "fetching the model"
	case l.failed != nil:
		return l.failed.Error()
	}
	return "the model has not been needed yet"
}

func (l *loader[M]) get() (M, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var none M
	if l.opened {
		return l.model, nil
	}
	if l.fetching {
		return none, fmt.Errorf("%w: %w", ErrUnavailable, ErrFetching)
	}
	m, err := l.open(l.dir, l.helper)
	if err == nil {
		l.model, l.opened, l.failed = m, true, nil
		return m, nil
	}
	if l.bundle == nil {
		l.failed = err
		return none, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if l.failed != nil && time.Since(l.failedAt) < fetchRetry {
		return none, fmt.Errorf("%w: %w", ErrUnavailable, l.failed)
	}

	l.fetching = true
	go l.fetch()
	return none, fmt.Errorf("%w: %w", ErrUnavailable, ErrFetching)
}

//nolint:contextcheck // detached on purpose: the call that started it is long gone
func (l *loader[M]) fetch() {
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
func (l *loader[M]) Close() {
	l.mu.Lock()
	m, opened := l.model, l.opened
	l.mu.Unlock()
	if opened {
		m.Close()
	}
}

// Lazy is SigLIP2, opened on first use and fetched first if it has to be.
type Lazy struct{ loader[*Model] }

// NewLazy is the model in dir, fetched from bundle when dir does not hold it.
func NewLazy(dir, helper string, bundle *Bundle, client *http.Client) *Lazy {
	return &Lazy{loader[*Model]{dir: dir, helper: helper, bundle: bundle, client: client, open: Open}}
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

// LazyFaces is the face models, opened on first use and fetched first if they
// have to be.
type LazyFaces struct{ loader[*FaceModels] }

// NewLazyFaces is the face models in dir, fetched from bundle when dir does
// not hold them.
func NewLazyFaces(dir, helper string, bundle *Bundle, client *http.Client) *LazyFaces {
	return &LazyFaces{loader[*FaceModels]{dir: dir, helper: helper, bundle: bundle, client: client, open: OpenFaces}}
}

// Detect is FaceModels.Detect, once there are models.
func (l *LazyFaces) Detect(ctx context.Context, bgr []float32) ([][]float32, error) {
	m, err := l.get()
	if err != nil {
		return nil, err
	}
	return m.Detect(ctx, bgr)
}

// Recognize is FaceModels.Recognize, once there are models.
func (l *LazyFaces) Recognize(ctx context.Context, rgb []float32) ([]float32, error) {
	m, err := l.get()
	if err != nil {
		return nil, err
	}
	return m.Recognize(ctx, rgb)
}
