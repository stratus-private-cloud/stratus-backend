package embed_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
)

const testBundle = "tiny-model.1"

func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func complete() map[string]string {
	files := map[string]string{}
	for _, f := range []string{"vision.ort", "text.ort", "tokenizer.bin", "LICENSE", "NOTICE"} {
		files[testBundle+"/"+f] = f + " bytes"
	}
	files[testBundle+"/NAME"] = testBundle + "\n"
	return files
}

func serve(t *testing.T, body []byte) (embed.Bundle, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(body)
	return embed.Bundle{Name: testBundle, URL: srv.URL, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}, &hits
}

func TestAFetchedBundleIsInPlaceAndNotFetchedTwice(t *testing.T) {
	b, hits := serve(t, archive(t, complete()))
	dir := filepath.Join(t.TempDir(), "models", testBundle)
	if err := embed.Fetch(t.Context(), http.DefaultClient, b, dir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "vision.ort")); err != nil || string(got) != "vision.ort bytes" {
		t.Errorf("vision.ort = %q, %v", got, err)
	}
	if err := embed.Fetch(t.Context(), http.DefaultClient, b, dir); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Errorf("the bundle was downloaded %d times, want once", hits.Load())
	}
	left, _ := filepath.Glob(dir + ".*")
	if len(left) != 0 {
		t.Errorf("a fetch left %v behind", left)
	}
}

func TestAFetchThatIsNotThePinnedBytesChangesNothing(t *testing.T) {
	for name, tamper := range map[string]func(*embed.Bundle){
		"another digest": func(b *embed.Bundle) { b.SHA256 = strings.Repeat("0", 64) },
		"another size":   func(b *embed.Bundle) { b.Size-- },
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := serve(t, archive(t, complete()))
			tamper(&b)
			dir := filepath.Join(t.TempDir(), testBundle)
			if err := embed.Fetch(t.Context(), http.DefaultClient, b, dir); err == nil {
				t.Fatal("a fetch of other bytes succeeded")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("the directory exists after a failed fetch: %v", err)
			}
		})
	}
}

func TestAnArchiveWithAnythingElseInItIsRefused(t *testing.T) {
	for name, edit := range map[string]func(map[string]string){
		"a path out":      func(f map[string]string) { f[testBundle+"/../escape"] = "x" },
		"another folder":  func(f map[string]string) { f["other/vision.ort"] = "x" },
		"an unknown file": func(f map[string]string) { f[testBundle+"/run.sh"] = "x" },
		"a file it lacks": func(f map[string]string) { delete(f, testBundle+"/text.ort") },
	} {
		t.Run(name, func(t *testing.T) {
			files := complete()
			edit(files)
			b, _ := serve(t, archive(t, files))
			dir := filepath.Join(t.TempDir(), testBundle)
			if err := embed.Fetch(t.Context(), http.DefaultClient, b, dir); err == nil {
				t.Fatal("the archive was unpacked")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("the directory exists after a refused archive: %v", err)
			}
		})
	}
}
