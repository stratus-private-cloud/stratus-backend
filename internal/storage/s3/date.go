package s3

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// dates is the client's transport, reading the Date off the answers to a
// request whose context asks for it. minio-go hands back no response headers.
type dates struct{ base http.RoundTripper }

func (d dates) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := d.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if rec, ok := req.Context().Value(responseDateKey{}).(*responseDate); ok && resp.StatusCode < 300 {
		if at, perr := http.ParseTime(resp.Header.Get("Date")); perr == nil {
			rec.set(at)
		}
	}
	return resp, nil
}

type responseDateKey struct{}

// responseDate keeps the latest Date answered: a multipart upload's last
// answer is the one that made the object.
type responseDate struct {
	mu sync.Mutex
	at time.Time
}

func withResponseDate(ctx context.Context, rec *responseDate) context.Context {
	return context.WithValue(ctx, responseDateKey{}, rec)
}

func (r *responseDate) set(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if at.After(r.at) {
		r.at = at
	}
}

func (r *responseDate) get() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.at
}
