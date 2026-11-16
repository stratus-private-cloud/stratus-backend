// Package semantic finds a photograph by what is in it (#505): vectors read
// off the decode a photograph's thumbnails are made from, or by a pass of its
// own for what no thumbnail pass will decode again, and a search that compares
// a query's vector with all of them.
//
// The vectors are internal/embed's and the table is db.Embeddings; what is
// here is when each is made, and the index in memory a search reads.
package semantic

import (
	"context"
	"errors"
	"fmt"
	"image"
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

// SimilarFloor is the least similarity a photograph needs to be shown as like
// another (#514). Two pictures are far closer than a picture and words: among
// the spike's 500 COCO photographs, chosen to have nothing to do with each
// other, pairs sharing no object still reach 0.68 at the median and 0.79 at the
// 99th percentile, and three photographs of horses were 0.79 to 0.80 apart. So
// the floor is under those, with room for the helper's int8 rounding: at 0.77 a
// photograph shows nine on average, two in three sharing an object with it,
// and seven in five hundred show none -- 0.80 had lost the horses.
const SimilarFloor = 0.77

// Dimensions is the size of SigLIP2 base's vectors.
const Dimensions = 768

// Encoder is the model, as the pass and the search use it.
type Encoder interface {
	Image(ctx context.Context, img image.Image) ([]float32, error)
	Text(ctx context.Context, query string) ([]float32, error)
}

// Pictures decodes a photograph for the pass, holding the decoder while fn
// runs: thumb.Service.Look.
type Pictures interface {
	Look(ctx context.Context, f db.File, fn func(image.Image) error) error
}

// Pipeline is what a vector's model name carries besides the model: how the
// picture it read was made. Vectors read off the grid's JPEG became vectors
// read off the decode it is written from (#519), and the two are not mixed in
// one index, so the name changed and the pass reads the library again.
const Pipeline = "+decoded"

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
	return &Service{store: store, pictures: pictures, encoder: encoder, model: model + Pipeline, idle: embed.Idle}
}

// Model is the name every vector this writes records.
func (s *Service) Model() string { return s.model }

// Batch reads up to Batch photographs that have no vector from this model,
// newest first, and answers how many it reached a verdict about. What is new
// is read beside its thumbnails (Read); this is the rest -- a library whose
// thumbnails were made before the switch was on, or by another model.
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
		err := s.pictures.Look(ctx, f, func(img image.Image) error { return s.Read(ctx, f, img) })
		if errors.Is(err, thumb.ErrNoThumbnail) {
			err = s.refuse(ctx, f, now)
		}
		switch {
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

// Wants implements thumb.Reader: whether f is one Batch would read.
func (s *Service) Wants(ctx context.Context, f db.File) (bool, error) {
	return s.store.WantsEmbedding(ctx, f.ID, s.model, time.Now().Add(-files.CacheRetention))
}

// Read implements thumb.Reader: one photograph's vector made of its picture,
// or written down as refused when the model would not take it.
func (s *Service) Read(ctx context.Context, f db.File, picture image.Image) error {
	now := time.Now()
	v, err := s.encoder.Image(ctx, picture)
	if err != nil {
		if errors.Is(err, embed.ErrUnavailable) || ctx.Err() != nil {
			return err
		}
		return s.refuse(ctx, f, now)
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

// refuse writes down that f has no vector, which the week brings round again.
func (s *Service) refuse(ctx context.Context, f db.File, now time.Time) error {
	return s.store.PutEmbedding(ctx, db.Embedding{
		FileID: f.ID, State: db.EmbeddingRefused, Model: s.model, BlobKey: f.BlobKey, At: now,
	})
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

// Similar answers the photographs closest to the one fileID is, closest first
// and at most limit, leaving it out. No model runs: the photograph's own
// vector is the query. One with no vector yet has nothing like it.
func (s *Service) Similar(ctx context.Context, fileID int64, limit int) ([]embed.Hit, error) {
	index, err := s.loaded(ctx)
	if err != nil {
		return nil, err
	}
	q, ok := index.Vector(fileID)
	if !ok {
		return nil, nil
	}
	hits := index.Search(q, SimilarFloor, limit+1)
	out := make([]embed.Hit, 0, len(hits))
	for _, h := range hits {
		if h.FileID != fileID && len(out) < limit {
			out = append(out, h)
		}
	}
	return out, nil
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
