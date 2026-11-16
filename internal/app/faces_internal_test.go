package app

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/config"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/faces"
	"github.com/stratus-private-cloud/stratus-backend/internal/files"
	"github.com/stratus-private-cloud/stratus-backend/internal/files/filestest"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
)

// faceModels finds nobody, or answers fail.
type faceModels struct{ fail error }

func (m faceModels) Detect(context.Context, []float32) ([][]float32, error) {
	return make([][]float32, 12), m.fail
}

func (m faceModels) Recognize(context.Context, []float32) ([]float32, error) {
	return nil, m.fail
}

// TestTheFacePassPacesItselfAndForgetsWhenOff is facesAhead on embedAhead's
// rules (#519), and the one thing it does with the switch off: delete every
// face there is.
func TestTheFacePassPacesItselfAndForgetsWhenOff(t *testing.T) {
	t.Parallel()
	blobs, meta := filestest.Backends(t)
	service := files.New(blobs, meta)
	thumbs := thumb.New(blobs, service, meta, "ffmpeg", t.TempDir())
	for i := range faces.Batch + 1 {
		writePhoto(t, service, meta, fmt.Sprintf("%d.jpg", i))
	}

	on := New(config.Config{Values: config.Values{Faces: true}}, "test", "2026-01-01T09:30:00Z")
	deps := Deps{Storage: blobs, Database: meta, Files: service, Thumbs: thumbs,
		Faces: faces.New(meta, blobs, thumbs, faceModels{}, "test-model")}
	if !on.facesOn(deps) || on.facesOn(Deps{}) {
		t.Error("facesOn does not follow the switch and the models")
	}
	if wait := on.facesAhead(t.Context(), deps); wait != thumbPause {
		t.Errorf("after a full batch the pass waits %v, want %v", wait, thumbPause)
	}
	if wait := on.facesAhead(t.Context(), deps); wait != 0 {
		t.Errorf("after the last photograph the pass waits %v, want the tick", wait)
	}

	writePhoto(t, service, meta, "late.jpg")
	for _, c := range []struct {
		fail error
		want time.Duration
	}{
		{fmt.Errorf("%w: %w", embed.ErrUnavailable, embed.ErrFetching), thumbBusyPause},
		{embed.ErrUnavailable, 0},
		{context.Canceled, 0},
	} {
		deps.Faces = faces.New(meta, blobs, thumbs, faceModels{fail: c.fail}, "test-model")
		if wait := on.facesAhead(t.Context(), deps); wait != c.want {
			t.Errorf("models answering %v: the pass waits %v, want %v", c.fail, wait, c.want)
		}
	}
	if wait := on.facesAhead(t.Context(), Deps{}); wait != 0 {
		t.Errorf("with nothing to run the pass waits %v", wait)
	}

	// Somebody, with a face and its picture, and then the switch goes off.
	photo, err := meta.FileByPath(t.Context(), "edu", "late.jpg")
	if err != nil {
		t.Fatal(err)
	}
	face, err := meta.AddFace(t.Context(), db.Face{FileID: photo.ID, BlobKey: photo.BlobKey, Vector: []byte{1}})
	if err != nil {
		t.Fatal(err)
	}
	crop := faces.CropKey(photo.BlobKey, face)
	if _, err := blobs.Put(t.Context(), crop, strings.NewReader("jpeg"), 4); err != nil {
		t.Fatal(err)
	}
	off := New(config.Config{}, "test", "2026-01-01T09:30:00Z")
	if wait := off.facesAhead(t.Context(), deps); wait != 0 {
		t.Errorf("with the switch off the pass waits %v", wait)
	}
	if _, err := meta.Face(t.Context(), face); err == nil {
		t.Error("a face outlived the switch going off")
	}
	if _, err := blobs.Stat(t.Context(), crop); err == nil {
		t.Error("the picture of a face outlived the switch going off")
	}
	off.forgetFaces(t.Context(), Deps{})
	if people(Deps{}) != nil || people(deps) == nil {
		t.Error("people does not follow whether there are faces")
	}
}

// TestFacesAreBuiltWhenTheirPartsAreThere is openSemantic's test for faces.
func TestFacesAreBuiltWhenTheirPartsAreThere(t *testing.T) {
	blobs, meta := filestest.Backends(t)
	service := files.New(blobs, meta)
	deps := Deps{Database: meta, Storage: blobs, Thumbs: thumb.New(blobs, service, meta, "ffmpeg", t.TempDir())}
	data := t.TempDir()

	t.Setenv("PATH", t.TempDir())
	if models, s := openFaces(config.Config{DataDir: data}, deps); models != nil || s != nil {
		t.Fatal("faces without stratus-embed")
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "stratus-embed"), []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	models, s := openFaces(config.Config{DataDir: data}, deps)
	if models == nil || s == nil || s.Model() != embed.Faces.Name+faces.Pipeline {
		t.Fatalf("with the helper and no directory named, the published models are not the ones used: %v", s)
	}

	mine := t.TempDir()
	if unopened, none := openFaces(config.Config{DataDir: data, FacesModel: mine}, deps); unopened != nil || none != nil {
		t.Error("a directory without the models in it was opened")
	}
	for _, f := range []string{"yunet.ort", "sface.ort"} {
		if err := os.WriteFile(filepath.Join(mine, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mine, "NAME"), []byte("my-faces\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	models, s = openFaces(config.Config{DataDir: data, FacesModel: mine}, deps)
	if models == nil || s == nil || s.Model() != "my-faces"+faces.Pipeline {
		t.Errorf("a directory somebody filled is not the one used: %v", s)
	}
}

// TestThePassesDecodeIsHandedToFacesOnlyWhileTheyAreOn: the reader the
// thumbnails' pass hands its picture to says no with the switch off, and with
// it on asks for the size faces read at and writes the scan.
func TestThePassesDecodeIsHandedToFacesOnlyWhileTheyAreOn(t *testing.T) {
	t.Parallel()
	blobs, meta := filestest.Backends(t)
	service := files.New(blobs, meta)
	thumbs := thumb.New(blobs, service, meta, "ffmpeg", t.TempDir())
	writePhoto(t, service, meta, "one.jpg")
	f, err := meta.FileByPath(t.Context(), "edu", "one.jpg")
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Storage: blobs, Database: meta, Faces: faces.New(meta, blobs, thumbs, faceModels{}, "test-model")}

	off := faceReader{a: New(config.Config{}, "test", "2026-01-01T09:30:00Z"), deps: deps}
	if ok, err := off.Wants(t.Context(), f); err != nil || ok {
		t.Errorf("with faces off the reader wants %v, %v", ok, err)
	}
	on := faceReader{a: New(config.Config{Values: config.Values{Faces: true}}, "test", "2026-01-01T09:30:00Z"), deps: deps}
	if ok, err := on.Wants(t.Context(), f); err != nil || !ok || on.Size() != faces.PictureSize {
		t.Fatalf("with faces on the reader wants %v, %v at %d", ok, err, on.Size())
	}
	if err := on.Read(t.Context(), f, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	if ok, _ := on.Wants(t.Context(), f); ok {
		t.Error("a photograph read for faces is still wanted")
	}
}
