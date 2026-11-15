package app

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/config"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/dbtest"
	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/files"
	"github.com/stratus-private-cloud/stratus-backend/internal/files/filestest"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
	"github.com/stratus-private-cloud/stratus-backend/internal/semantic"
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
	if model == nil || s == nil || s.Model() != embed.SigLIP2.Name {
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
	if model == nil || s == nil || s.Model() != "my-model" {
		t.Errorf("a directory somebody filled is not the one used: %v", s)
	}
}
