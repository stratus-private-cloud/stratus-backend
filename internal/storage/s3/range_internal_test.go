package s3

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/stratus-private-cloud/stratus-backend/internal/storage"
)

// rangedBucket is one object answering GET and HEAD as S3 does, counting the
// requests it is sent.
func rangedBucket(t *testing.T, body string) (*Store, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var gets, heads atomic.Int32
	modified := time.Now().UTC().Format(http.TimeFormat)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"x"`)
		w.Header().Set("Last-Modified", modified)
		if r.Method == http.MethodHead {
			heads.Add(1)
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			return
		}
		gets.Add(1)
		spec, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes=")
		if !ok {
			_, _ = io.WriteString(w, body)
			return
		}
		from, to, _ := strings.Cut(spec, "-")
		off, _ := strconv.Atoi(from)
		last := len(body) - 1
		if to != "" {
			last, _ = strconv.Atoi(to)
			last = min(last, len(body)-1)
		}
		if off >= len(body) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			_, _ = io.WriteString(w, `<Error><Code>InvalidRange</Code><Message>no</Message></Error>`)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, last, len(body)))
		w.Header().Set("Content-Length", strconv.Itoa(last-off+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, body[off:last+1])
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4("k", "s", ""), Region: "us-east-1"})
	if err != nil {
		t.Fatal(err)
	}
	return &Store{client: client, core: minio.Core{Client: client}, bucket: "bucket", partSize: defaultPartSize}, &gets, &heads
}

// TestARangeFromAnOffsetIsOneRequest is #562: a HEAD for the size and then the
// GET, on every seek in a video. The size comes from Content-Range now.
func TestARangeFromAnOffsetIsOneRequest(t *testing.T) {
	t.Parallel()
	const body = "0123456789"
	store, gets, heads := rangedBucket(t, body)

	for _, tc := range []struct {
		rng  storage.Range
		want string
	}{
		{storage.From(4), "456789"},
		{storage.All(), body},
		{storage.Slice(2, 3), "234"},
		{storage.Slice(8, 50), "89"},
	} {
		r, info, err := store.Get(t.Context(), "video/x.mp4", tc.rng)
		if err != nil {
			t.Fatalf("Get(%+v) = %v", tc.rng, err)
		}
		got, _ := io.ReadAll(r)
		_ = r.Close()
		if string(got) != tc.want || info.Size != int64(len(body)) {
			t.Errorf("Get(%+v) = %q of %d, want %q of %d", tc.rng, got, info.Size, tc.want, len(body))
		}
	}
	if heads.Load() != 0 || gets.Load() != 4 {
		t.Errorf("four ranged reads were %d GETs and %d HEADs, want four and none", gets.Load(), heads.Load())
	}

	// At the end is zero bytes and past it is refused, which only the size
	// can tell apart: those two ask for it, as every range used to.
	r, _, err := store.Get(t.Context(), "video/x.mp4", storage.From(10))
	if err != nil {
		t.Fatalf("Get at the end = %v", err)
	}
	if got, _ := io.ReadAll(r); len(got) != 0 {
		t.Errorf("Get at the end read %q", got)
	}
	if _, _, err := store.Get(t.Context(), "video/x.mp4", storage.From(11)); !errors.Is(err, storage.ErrInvalidRange) {
		t.Errorf("Get past the end = %v, want ErrInvalidRange", err)
	}
}
