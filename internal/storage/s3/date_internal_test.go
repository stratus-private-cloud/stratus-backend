package s3

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// fakeStore answers PUT and HEAD and counts the HEADs. sendDate false answers
// with no Date at all, which net/http otherwise always adds.
func fakeStore(t *testing.T, date time.Time, sendDate bool) (*Store, *atomic.Int32) {
	t.Helper()
	var heads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sendDate {
			w.Header().Set("Date", date.Format(http.TimeFormat))
		} else {
			w.Header()["Date"] = nil
		}
		switch r.Method {
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", `"x"`)
		case http.MethodHead:
			heads.Add(1)
			w.Header().Set("ETag", `"x"`)
			w.Header().Set("Content-Length", "5")
			w.Header().Set("Last-Modified", date.Format(http.TimeFormat))
		}
	}))
	t.Cleanup(srv.Close)

	transport, err := minio.DefaultTransport(false)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	client, err := minio.New(u.Host, &minio.Options{
		Creds:     credentials.NewStaticV4("k", "s", ""),
		Region:    "us-east-1",
		Transport: dates{transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Store{client: client, core: minio.Core{Client: client}, bucket: "bucket", partSize: defaultPartSize}, &heads
}

// TestAPutTakesItsTimeFromTheAnswer is #563: a thumbnail's Put was a PUT and
// a HEAD, for a time only an uploaded file's row reads.
func TestAPutTakesItsTimeFromTheAnswer(t *testing.T) {
	t.Parallel()
	date := time.Date(2026, 11, 22, 15, 4, 5, 0, time.UTC)
	store, heads := fakeStore(t, date, true)

	info, err := store.Put(t.Context(), "image/x.jpg", strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if n := heads.Load(); n != 0 {
		t.Errorf("HEADs = %d, want 0", n)
	}
	if !info.ModTime.Equal(date) || info.Size != 5 || info.Key != "image/x.jpg" {
		t.Errorf("Put = %+v, want x.jpg, 5 bytes, %v", info, date)
	}
}

// TestAPutAnsweredWithNoDateAsks: a store that sends no Date is asked rather
// than handed this machine's clock.
func TestAPutAnsweredWithNoDateAsks(t *testing.T) {
	t.Parallel()
	date := time.Date(2026, 11, 22, 15, 4, 5, 0, time.UTC)
	store, heads := fakeStore(t, date, false)

	info, err := store.Put(t.Context(), "image/x.jpg", strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if n := heads.Load(); n != 1 {
		t.Errorf("HEADs = %d, want 1", n)
	}
	if !info.ModTime.Equal(date) {
		t.Errorf("ModTime = %v, want the stat's %v", info.ModTime, date)
	}
}
