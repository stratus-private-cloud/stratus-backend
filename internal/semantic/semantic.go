// Package semantic finds a photograph by what is in it (#505): a pass that
// reads every photograph's vector off its grid picture, as the indexer's
// third half, and a search that compares a query's vector with all of them.
//
// The vectors are internal/embed's and the table is db.Embeddings; what is
// here is when each is made, and the index in memory a search reads.
package semantic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // the grid's pictures are JPEG
	"log/slog"
	"sync"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/files"
	"github.com/stratus-private-cloud/stratus-backend/internal/media"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
)

// Batch is how many photographs one call of the pass reads. A vector is a
// second of CPU on a small machine, so this is the thumbnail pass's number.
const Batch = 8

// Floor is the least similarity a photograph needs to be an answer.
// SigLIP2's cosines are small and close together: on the spike's 500
// photographs 0.06 kept 0.79 of what a Spanish query should find with 0.61 of
// what it showed right, by labels that miss half of what is in a picture, and
// the order puts the best first.
const Floor = 0.06

// Dimensions is the size of SigLIP2 base's vectors.
const Dimensions = 768

// Encoder is the model, as the pass and the search use it.
type Encoder interface {
	Image(ctx context.Context, img image.Image) ([]float32, error)
	Text(ctx context.Context, query string) ([]float32, error)
}

// Pictures is where a photograph's grid picture comes from, and the one
// question of whether anybody is looking.
type Pictures interface {
	Picture(ctx context.Context, f db.File) ([]byte, error)
	WhenIdle(fn func() error) error
}

// Service is the pass and the search over one model.
type Service struct {
	store    db.Embeddings
	pictures Pictures
	encoder  Encoder
	model    string
	idle     time.Duration

	mu    sync.Mutex
	index *embed.Index
	timer *time.Timer
}

// New wires the service for the model named model.
func New(store db.Embeddings, pictures Pictures, encoder Encoder, model string) *Service {
	return &Service{store: store, pictures: pictures, encoder: encoder, model: model, idle: embed.Idle}
}

// Model is the name every vector this writes records.
func (s *Service) Model() string { return s.model }

// Batch reads up to Batch photographs that have no vector from this model,
// newest first, and answers how many it reached a verdict about.
//
// media.ErrBusy is the pass standing aside for whoever is looking, as the
// thumbnails' does, and embed.ErrUnavailable a helper that could not run: in
// both the row is left alone and the caller decides when to come back.
func (s *Service) Batch(ctx context.Context, now time.Time) (int, error) {
	pending, err := s.store.PendingEmbeddings(ctx, s.model, now.Add(-files.CacheRetention), Batch)
	if err != nil {
		return 0, fmt.Errorf("find photographs to read: %w", err)
	}
	done := 0
	for _, f := range pending {
		switch err := s.read(ctx, f, now); {
		case errors.Is(err, media.ErrBusy), errors.Is(err, embed.ErrUnavailable),
			errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return done, err
		case errors.Is(err, db.ErrNotFound):
			// Deleted while it was being read.
		case err != nil:
			slog.InfoContext(ctx, "reading a photograph's content", "path", f.Path, "err", err)
			continue
		}
		done++
	}
	return done, nil
}

// read makes one photograph's vector, or writes down why there is none.
func (s *Service) read(ctx context.Context, f db.File, now time.Time) error {
	refuse := func() error {
		return s.store.PutEmbedding(ctx, db.Embedding{
			FileID: f.ID, State: db.EmbeddingRefused, Model: s.model, BlobKey: f.BlobKey, At: now,
		})
	}

	picture, err := s.pictures.Picture(ctx, f)
	switch {
	case errors.Is(err, thumb.ErrNoThumbnail):
		return refuse()
	case err != nil:
		return err
	}
	img, _, err := image.Decode(bytes.NewReader(picture))
	if err != nil {
		// A picture this server wrote that it cannot read back is a verdict
		// on nothing but the picture, and the week brings it round again.
		return refuse()
	}

	var v []float32
	err = s.pictures.WhenIdle(func() error {
		var ierr error
		v, ierr = s.encoder.Image(ctx, img)
		return ierr
	})
	if err != nil {
		if errors.Is(err, media.ErrBusy) || errors.Is(err, embed.ErrUnavailable) || ctx.Err() != nil {
			return err
		}
		return refuse()
	}
	if len(v) != Dimensions {
		return fmt.Errorf("%w: %d dimensions", embed.ErrDimension, len(v))
	}

	stored := embed.Quantize(v)
	err = s.store.PutEmbedding(ctx, db.Embedding{
		FileID: f.ID, State: db.EmbeddingMade, Model: s.model, BlobKey: f.BlobKey, Vector: stored, At: now,
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.index != nil {
		_ = s.index.Put(f.ID, stored)
	}
	s.mu.Unlock()
	return nil
}

// Search answers the photographs closest to query, closest first, and at
// most limit of them.
func (s *Service) Search(ctx context.Context, query string, limit int) ([]embed.Hit, error) {
	index, err := s.loaded(ctx)
	if err != nil {
		return nil, err
	}
	q, err := s.encoder.Text(ctx, query)
	if err != nil {
		return nil, err
	}
	return index.Search(q, Floor, limit), nil
}

// loaded is the index, read from the table on the first search and dropped
// after idle without one: tens of megabytes nobody needs between searches.
//
// The lock is held through the read, so two searches arriving at once read
// the table once.
func (s *Service) loaded(ctx context.Context) (*embed.Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.idle, s.drop)
	if s.index != nil {
		return s.index, nil
	}

	index := embed.NewIndex(Dimensions)
	err := s.store.EachEmbedding(ctx, s.model, func(id int64, v []byte) error {
		if err := index.Put(id, v); err != nil {
			// One bad row is not worth a search that answers nothing.
			slog.WarnContext(ctx, "a stored vector does not fit", "file", id, "err", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the vectors: %w", err)
	}
	s.index = index
	return index, nil
}

func (s *Service) drop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.index = nil
}

// State is what a page says about the model: nothing when it is ready or the
// encoder cannot say, and otherwise why it is not.
func (s *Service) State() string {
	if st, ok := s.encoder.(interface{ State() string }); ok {
		return st.State()
	}
	return ""
}

// Counts is how far the pass has got, for /status.
func (s *Service) Counts(ctx context.Context) (db.EmbeddingCounts, error) {
	return s.store.EmbeddingCounts(ctx, s.model)
}
