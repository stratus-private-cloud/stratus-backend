package embed_test

import (
	"errors"
	"image"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
)

func waitWhileFetching(t *testing.T, l *embed.Lazy) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for l.State() == "fetching the model" {
		if time.Now().After(deadline) {
			t.Fatal("the fetch never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestALazyModelIsFetchedOnItsOwnAndThenOpened(t *testing.T) {
	b, _ := serve(t, archive(t, complete()))
	l := embed.NewLazy(filepath.Join(t.TempDir(), testBundle), "", &b, http.DefaultClient)
	t.Cleanup(l.Close)
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))

	if _, err := l.Image(t.Context(), img); !errors.Is(err, embed.ErrFetching) || !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("the first call = %v, want ErrFetching", err)
	}
	waitWhileFetching(t, l)

	// Opened now; what answers is the missing helper, not the fetch.
	_, err := l.Image(t.Context(), img)
	if errors.Is(err, embed.ErrFetching) || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("after the fetch = %v, want the helper missing", err)
	}
	if l.State() != "" {
		t.Errorf("State = %q once the model is open", l.State())
	}
}

func TestAFailedFetchIsSaidAndNotRetriedAtOnce(t *testing.T) {
	b, hits := serve(t, archive(t, complete()))
	b.SHA256 = strings.Repeat("0", 64)
	l := embed.NewLazy(filepath.Join(t.TempDir(), testBundle), "", &b, http.DefaultClient)
	t.Cleanup(l.Close)

	_, _ = l.Text(t.Context(), "perro")
	waitWhileFetching(t, l)
	if !strings.Contains(l.State(), "not the bytes pinned") {
		t.Errorf("State = %q, want the reason", l.State())
	}
	if _, err := l.Text(t.Context(), "perro"); errors.Is(err, embed.ErrFetching) || !errors.Is(err, embed.ErrUnavailable) {
		t.Errorf("a call after a failed fetch = %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("the bundle was asked for %d times, want once within the hour", hits.Load())
	}
}

func TestLazyFaceModelsAreFetchedAsTheOtherBundleIs(t *testing.T) {
	files := map[string]string{}
	for _, f := range embed.Faces.Files {
		files[testBundle+"/"+f] = f + " bytes"
	}
	files[testBundle+"/NAME"] = testBundle + "\n"
	b, _ := serve(t, archive(t, files))
	b.Files = embed.Faces.Files
	l := embed.NewLazyFaces(filepath.Join(t.TempDir(), testBundle), "", &b, http.DefaultClient)
	t.Cleanup(l.Close)

	if _, err := l.Detect(t.Context(), nil); !errors.Is(err, embed.ErrFetching) {
		t.Fatalf("the first call = %v, want ErrFetching", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for l.State() == "fetching the model" {
		if time.Now().After(deadline) {
			t.Fatal("the fetch never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := l.Recognize(t.Context(), nil); errors.Is(err, embed.ErrFetching) || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("after the fetch = %v, want the helper missing", err)
	}
}
