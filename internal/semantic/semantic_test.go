package semantic_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/sqlite"
	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/media"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
	"github.com/stratus-private-cloud/stratus-backend/internal/semantic"
)

// The fakes see colour: a picture's vector points at its red, green or blue,
// and a query names one of the three. That is enough to tell whether a search
// finds what the pass read.

var axis = map[string]int{"red": 0, "green": 1, "blue": 2}

type encoder struct {
	mu     sync.Mutex
	images int
	fail   error
}

func (e *encoder) Image(_ context.Context, img image.Image) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail != nil {
		return nil, e.fail
	}
	e.images++
	r, g, b, _ := img.At(0, 0).RGBA()
	v := make([]float32, semantic.Dimensions)
	switch {
	case r > g && r > b:
		v[0] = 1
	case g > b:
		v[1] = 1
	default:
		v[2] = 1
	}
	return v, nil
}

func (e *encoder) Text(_ context.Context, q string) ([]float32, error) {
	v := make([]float32, semantic.Dimensions)
	v[axis[q]] = 1
	return v, nil
}

type pictures struct {
	colours map[int64]color.Color
	busy    bool
	refuse  map[int64]bool
}

func (p *pictures) Picture(_ context.Context, f db.File) ([]byte, error) {
	if p.busy {
		return nil, media.ErrBusy
	}
	if p.refuse[f.ID] {
		return nil, thumb.ErrNoThumbnail
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range 64 {
		img.Set(i%8, i/8, p.colours[f.ID])
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, nil); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (p *pictures) WhenIdle(fn func() error) error {
	if p.busy {
		return media.ErrBusy
	}
	return fn()
}

type fixture struct {
	store    *sqlite.Store
	pictures *pictures
	encoder  *encoder
	service  *semantic.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "stratus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := &pictures{colours: map[int64]color.Color{}, refuse: map[int64]bool{}}
	e := &encoder{}
	return &fixture{store: store, pictures: p, encoder: e, service: semantic.New(store, p, e, "fake-1")}
}

func (fx *fixture) photo(t *testing.T, name string, c color.Color) db.File {
	t.Helper()
	f, err := fx.store.PutFile(t.Context(), db.File{
		OwnerID: "edu", Path: name, BlobKey: "blobs/" + name, Size: 10,
		MTime: time.Now(), ETag: `"` + name + `"`, MIMEType: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.store.PutMedia(t.Context(), db.Media{
		FileID: f.ID, Kind: db.KindImage, IndexedAt: time.Now(), Version: 1, ETag: f.ETag,
	}); err != nil {
		t.Fatal(err)
	}
	fx.pictures.colours[f.ID] = c
	return f
}

func TestAPassReadsWhatASearchFinds(t *testing.T) {
	fx := newFixture(t)
	red := fx.photo(t, "red.jpg", color.RGBA{R: 255, A: 255})
	fx.photo(t, "green.jpg", color.RGBA{G: 255, A: 255})
	blue := fx.photo(t, "blue.jpg", color.RGBA{B: 255, A: 255})
	refused := fx.photo(t, "broken.jpg", color.Black)
	fx.pictures.refuse[refused.ID] = true

	done, err := fx.service.Batch(t.Context(), time.Now())
	if err != nil || done != 4 {
		t.Fatalf("Batch = %d, %v; want four verdicts", done, err)
	}
	if again, berr := fx.service.Batch(t.Context(), time.Now()); berr != nil || again != 0 {
		t.Fatalf("a second Batch = %d, %v; want nothing left", again, berr)
	}
	counts, err := fx.service.Counts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if counts != (db.EmbeddingCounts{Files: 4, Made: 3, Refused: 1}) {
		t.Errorf("Counts = %+v", counts)
	}

	hits, err := fx.service.Search(t.Context(), "red", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].FileID != red.ID {
		t.Errorf("Search(red) = %+v, want the red photograph alone", hits)
	}

	// A photograph read while the index is in memory is in the next search
	// without reading the table again.
	late := fx.photo(t, "late-blue.jpg", color.RGBA{B: 255, A: 255})
	if _, err = fx.service.Batch(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	hits, err = fx.service.Search(t.Context(), "blue", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || (hits[0].FileID != blue.ID && hits[1].FileID != blue.ID) ||
		(hits[0].FileID != late.ID && hits[1].FileID != late.ID) {
		t.Errorf("Search(blue) = %+v, want both blue photographs", hits)
	}
}

func TestAPassStandsAsideAndWritesNothing(t *testing.T) {
	fx := newFixture(t)
	fx.photo(t, "red.jpg", color.RGBA{R: 255, A: 255})

	fx.pictures.busy = true
	if _, err := fx.service.Batch(t.Context(), time.Now()); !errors.Is(err, media.ErrBusy) {
		t.Fatalf("Batch while somebody is looking = %v, want media.ErrBusy", err)
	}
	fx.pictures.busy = false
	fx.encoder.fail = embed.ErrUnavailable
	if _, err := fx.service.Batch(t.Context(), time.Now()); !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("Batch with no helper = %v, want embed.ErrUnavailable", err)
	}

	counts, err := fx.service.Counts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if counts.Waiting() != 1 {
		t.Errorf("Counts = %+v, want the photograph still waiting", counts)
	}
}
