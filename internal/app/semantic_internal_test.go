package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/config"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/dbtest"
	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/files"
	"github.com/stratus-private-cloud/stratus-backend/internal/files/filestest"
	"github.com/stratus-private-cloud/stratus-backend/internal/media"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
	"github.com/stratus-private-cloud/stratus-backend/internal/semantic"
	"github.com/stratus-private-cloud/stratus-backend/internal/storage"
)

// encoder answers every picture with the same vector, or with fail.
type encoder struct{ fail error }

func (e encoder) Image(context.Context, image.Image) ([]float32, error) {
	if e.fail != nil {
		return nil, e.fail
	}
	v := make([]float32, semantic.Dimensions)
	v[0] = 1
	return v, nil
}

func (e encoder) Text(ctx context.Context, _ string) ([]float32, error) { return e.Image(ctx, nil) }

// TestTheContentPassPacesItself is embedAhead's half of what thumbsAhead
// decides (#505): straight back while a batch fills, the tick otherwise, a
// minute while the model is on its way, and nothing at all with the switch
// off or no model to run.
func TestTheContentPassPacesItself(t *testing.T) {
	t.Parallel()
	blobs, meta := filestest.Backends(t)
	service := files.New(blobs, meta)
	thumbs := thumb.New(blobs, service, meta, "ffmpeg", t.TempDir())
	for i := range semantic.Batch + 1 {
		writePhoto(t, service, meta, fmt.Sprintf("%d.jpg", i))
	}

	a := New(config.Config{Values: config.Values{SemanticSearch: true}}, "test", "2026-01-01T09:30:00Z")
	deps := Deps{Storage: blobs, Database: meta, Files: service, Thumbs: thumbs,
		Semantic: semantic.New(meta, thumbs, encoder{}, "test-model")}

	if wait := a.embedAhead(t.Context(), deps); wait != thumbPause {
		t.Errorf("after a full batch the pass waits %v, want %v", wait, thumbPause)
	}
	if wait := a.embedAhead(t.Context(), deps); wait != 0 {
		t.Errorf("after the last photograph the pass waits %v, want the tick", wait)
	}

	writePhoto(t, service, meta, "late.jpg")
	// In this order, since the last is a refusal and leaves nothing pending.
	for _, c := range []struct {
		fail error
		want time.Duration
	}{
		{fmt.Errorf("%w: %w", embed.ErrUnavailable, embed.ErrFetching), thumbBusyPause},
		{embed.ErrUnavailable, 0},
		{context.Canceled, 0},
		{os.ErrPermission, 0},
	} {
		deps.Semantic = semantic.New(meta, thumbs, encoder{fail: c.fail}, "test-model")
		if wait := a.embedAhead(t.Context(), deps); wait != c.want {
			t.Errorf("a model answering %v: the pass waits %v, want %v", c.fail, wait, c.want)
		}
	}

	deps.Semantic = semantic.New(dbtest.FailOn(t, meta, "PendingEmbeddings"), thumbs, encoder{}, "test-model")
	if wait := a.embedAhead(t.Context(), deps); wait != 0 {
		t.Errorf("a pass that could not read the queue waits %v, want the tick", wait)
	}

	off := New(config.Config{}, "test", "2026-01-01T09:30:00Z")
	if wait := off.embedAhead(t.Context(), deps); wait != 0 || off.semanticOn(deps) {
		t.Errorf("the pass ran with the switch off and waits %v", wait)
	}
	if a.semanticOn(Deps{}) {
		t.Error("the pass is on with no model to run")
	}
	if contents(Deps{}) != nil || contents(deps) == nil {
		t.Error("contents does not follow whether there is a search by content")
	}
}

// TestASearchByContentIsBuiltWhenItsPartsAreThere: no helper is no search, a
// directory somebody named is opened and must be whole, and otherwise the
// published model is fetched into the data directory.
func TestASearchByContentIsBuiltWhenItsPartsAreThere(t *testing.T) {
	blobs, meta := filestest.Backends(t)
	service := files.New(blobs, meta)
	deps := Deps{Database: meta, Thumbs: thumb.New(blobs, service, meta, "ffmpeg", t.TempDir())}
	data := t.TempDir()

	t.Setenv("PATH", t.TempDir())
	if model, s := openSemantic(config.Config{DataDir: data}, deps); model != nil || s != nil {
		t.Fatal("a search by content without stratus-embed")
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "stratus-embed"), []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	model, s := openSemantic(config.Config{DataDir: data}, deps)
	if model == nil || s == nil || s.Model() != embed.SigLIP2.Name+semantic.Pipeline {
		t.Fatalf("with the helper and no directory named, the published model is not the one used: %v", s)
	}

	mine := t.TempDir()
	if unopened, none := openSemantic(config.Config{DataDir: data, EmbedModel: mine}, deps); unopened != nil || none != nil {
		t.Error("a directory without a model in it was opened")
	}
	for _, f := range []string{"vision.ort", "text.ort", "tokenizer.bin"} {
		if err := os.WriteFile(filepath.Join(mine, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mine, "NAME"), []byte("my-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model, s = openSemantic(config.Config{DataDir: data, EmbedModel: mine}, deps)
	if model == nil || s == nil || s.Model() != "my-model"+semantic.Pipeline {
		t.Errorf("a directory somebody filled is not the one used: %v", s)
	}
}

// counting is a store that counts what it is asked, which is what a
// photograph costs on S3.
type counting struct {
	storage.Storage
	mu    sync.Mutex
	gets  map[string]int
	stats int
}

func (c *counting) Get(ctx context.Context, key string, rng storage.Range) (io.ReadCloser, storage.ObjectInfo, error) {
	c.mu.Lock()
	c.gets[key]++
	c.mu.Unlock()
	return c.Storage.Get(ctx, key, rng)
}

func (c *counting) Stat(ctx context.Context, key string) (storage.ObjectInfo, error) {
	c.mu.Lock()
	c.stats++
	c.mu.Unlock()
	return c.Storage.Stat(ctx, key)
}

// TestAPhotographIsReadOnce is #519's whole point: a photograph that has just
// arrived is indexed, drawn and read for its content out of one request to
// the store, and none of them asks it anything else first.
func TestAPhotographIsReadOnce(t *testing.T) {
	t.Parallel()
	real, meta := filestest.Backends(t)
	blobs := &counting{Storage: real, gets: map[string]int{}}
	service := files.New(blobs, meta)
	thumbs := thumb.New(blobs, service, meta, "ffmpeg", t.TempDir())

	a := New(config.Config{Values: config.Values{ThumbsAhead: true, SemanticSearch: true}}, "test", "2026-01-01T09:30:00Z")
	deps := Deps{Storage: blobs, Database: meta, Files: service, Thumbs: thumbs,
		Indexer:  media.NewIndexer(service, meta, t.TempDir(), ""),
		Semantic: semantic.New(meta, thumbs, encoder{}, "test-model")}
	thumbs.Also(contentReader{a: a, deps: deps})

	var body bytes.Buffer
	if err := jpeg.Encode(&body, image.NewRGBA(image.Rect(0, 0, 640, 480)), nil); err != nil {
		t.Fatal(err)
	}
	f, err := service.Write(t.Context(), "edu", "one.jpg", bytes.NewReader(body.Bytes()), int64(body.Len()), "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	blobs.gets, blobs.stats = map[string]int{}, 0

	if !a.arrived(t.Context(), deps, f) {
		t.Fatal("arrived says the process is stopping")
	}

	if got := blobs.gets[f.BlobKey]; got != 1 || len(blobs.gets) != 1 || blobs.stats != 0 {
		t.Errorf("the store was asked %v and %d Stats, want the original once and nothing else", blobs.gets, blobs.stats)
	}
	states, err := meta.MediaStates(t.Context(), []int64{f.ID})
	if err != nil {
		t.Fatal(err)
	}
	if states[f.ID].Thumb != db.ThumbMade {
		t.Errorf("the pictures were not made: %+v", states[f.ID])
	}
	if wanted, err := meta.WantsEmbedding(t.Context(), f.ID, deps.Semantic.Model(), time.Now()); err != nil || wanted {
		t.Errorf("the content was not read: %v, %v", wanted, err)
	}
	if m, err := meta.MediaByFile(t.Context(), f.ID); err != nil || m.Kind != db.KindImage || m.Error != "" {
		t.Errorf("the metadata was not read: %+v, %v", m, err)
	}
}
