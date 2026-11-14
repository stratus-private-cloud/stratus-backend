package web

import (
	"bytes"
	"crypto/rand"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Post/redirect/get for every form at once (#501). A form a browser submits
// is answered with a redirect, whatever the handler made of it: one that
// redirected already keeps its own, and one that drew a page -- the link a
// share made, a form with its problems marked, an error -- has that page kept
// here and is sent to it at its own address with ?flash=<id>. Back and reload
// then land on a GET, which shows the page again and sends nothing.
//
// **Kept in memory, and that is the trade.** The page is the handler's own
// output, so no handler had to learn a second way to say what went wrong; and
// it is somebody's form for the next few minutes, which is not worth a table.
// A restart forgets it, and the address then answers as it would without the
// parameter: the empty form, the folder, the page asked for.
//
// Only for a browser submitting a form -- a navigation, by the header #279
// already trusts -- and only behind a session: htmx and WebDAV clients get
// their answers as before, and a page kept here is shown to the session that
// posted it and to nobody else. The login form is the exception that has no
// session yet, and its page holds nothing but the form.
type stash struct {
	mu    sync.Mutex
	pages map[string]keptPage
	order []string
}

type keptPage struct {
	user, path string
	status     int
	header     http.Header
	body       []byte
	at         time.Time
}

const (
	flashParam = "flash"
	// keptFor is how long a reload still shows the page a form drew.
	keptFor = 15 * time.Minute
	// keptMost bounds the memory: a page each, for the forms of the last few
	// minutes of one person's use.
	keptMost = 32
)

func newStash() *stash { return &stash{pages: map[string]keptPage{}} }

func (s *stash) put(p keptPage) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rand.Text()
	s.pages[id] = p
	s.order = append(s.order, id)
	for len(s.order) > keptMost {
		delete(s.pages, s.order[0])
		s.order = s.order[1:]
	}
	return id
}

func (s *stash) get(id string, now time.Time) (keptPage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pages[id]
	if !ok || now.Sub(p.at) > keptFor {
		return keptPage{}, false
	}
	return p, true
}

// postRedirectGet is the middleware: it answers a kept page to the GET that
// asks for it, and turns a form's drawn answer into a redirect to one.
func (h *handler) postRedirectGet(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Has(flashParam):
			if h.serveKept(w, r) {
				return
			}
		case r.Method == http.MethodPost && r.Header.Get("Sec-Fetch-Mode") == "navigate":
			h.keepWhatIsDrawn(w, r, next)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) serveKept(w http.ResponseWriter, r *http.Request) bool {
	p, ok := h.pages.get(r.URL.Query().Get(flashParam), time.Now())
	if !ok || p.path != r.URL.Path {
		return false
	}
	if p.user != "" {
		if user, err := h.session(r); err != nil || user != p.user {
			return false
		}
	}
	for k, v := range p.header {
		w.Header()[k] = v
	}
	w.WriteHeader(p.status)
	_, _ = w.Write(p.body)
	return true
}

func (h *handler) keepWhatIsDrawn(w http.ResponseWriter, r *http.Request, next http.Handler) {
	user, _ := h.session(r)
	rec := &recorded{header: http.Header{}, status: http.StatusOK}
	next.ServeHTTP(rec, r)

	drawn := rec.status < 300 || rec.status >= 400
	if !drawn || (user == "" && r.URL.Path != "/login") {
		rec.replay(w)
		return
	}
	header := rec.header.Clone()
	// What the response set stays on the redirect, which is where a browser
	// takes it; the page is everything else.
	for _, c := range header.Values("Set-Cookie") {
		w.Header().Add("Set-Cookie", c)
	}
	header.Del("Set-Cookie")
	header.Del("Content-Length")
	id := h.pages.put(keptPage{
		user: user, path: r.URL.Path, status: rec.status, header: header, body: rec.body.Bytes(), at: time.Now(),
	})
	q := r.URL.Query()
	q.Set(flashParam, id)
	redirectLocal(w, r, (&url.URL{Path: r.URL.Path, RawQuery: q.Encode()}).String())
}

// recorded is a response held back until it is known whether it is a page.
type recorded struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorded) Header() http.Header { return r.header }

func (r *recorded) WriteHeader(status int) {
	if !r.wrote {
		r.status, r.wrote = status, true
	}
}

func (r *recorded) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}

func (r *recorded) replay(w http.ResponseWriter) {
	for k, v := range r.header {
		w.Header()[k] = v
	}
	w.WriteHeader(r.status)
	_, _ = w.Write(r.body.Bytes())
}
