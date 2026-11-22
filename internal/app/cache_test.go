package app_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/app"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/files"
	"github.com/stratus-private-cloud/stratus-backend/internal/files/filestest"
	"github.com/stratus-private-cloud/stratus-backend/internal/media"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
)

// TestNothingBehindAPasswordIsLeftForACDN is #587: a response with no
// Cache-Control may be kept by a CDN in front of the server by its extension
// and handed to whoever asks next. Every surface is walked, and what is behind
// a password says private or no-store; what is public on purpose still is.
func TestNothingBehindAPasswordIsLeftForACDN(t *testing.T) {
	t.Parallel()
	blobs, meta := filestest.Backends(t)
	service := files.New(blobs, meta)

	taken := time.Date(2024, 6, 15, 10, 0, 0, 0, time.UTC)
	index := func(name string, m db.Media) db.File {
		t.Helper()
		f, err := service.Write(t.Context(), "edu", name, strings.NewReader("bytes"), 5, "")
		if err != nil {
			t.Fatal(err)
		}
		m.FileID, m.IndexedAt, m.Version = f.ID, time.Now(), media.Version
		if err := meta.PutMedia(t.Context(), m); err != nil {
			t.Fatal(err)
		}
		return f
	}
	index("notes.txt", db.Media{Kind: db.KindOther})
	index("IMG_0001.jpg", db.Media{Kind: db.KindImage, TakenAt: taken})
	track := index("song.mp3", db.Media{Kind: db.KindAudio, Title: "Song", Artist: "Band", AlbumArtist: "Band", Album: "Record"})

	const password = "an example password"
	cfg := runConfig(t, map[string]string{"STRATUS_USERNAME": "edu", "STRATUS_PASSWORD": password})
	h := app.New(cfg, "test", "2026-01-01T09:30:00Z").Handler(app.Deps{
		Storage: blobs, Database: meta, Files: service,
		Thumbs: thumb.New(blobs, service, meta, "ffmpeg", t.TempDir()),
	})
	send := func(method, target, body string, basic bool, headers ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
		if basic {
			req.SetBasicAuth("edu", password)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	const event = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//t//EN\r\nBEGIN:VEVENT\r\nUID:e1\r\nDTSTAMP:20240601T000000Z\r\nDTSTART:20240601T100000Z\r\nSUMMARY:x\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	const card = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c1\r\nFN:Ana\r\nN:;Ana;;;\r\nEND:VCARD\r\n"
	send("PROPFIND", "/calendars/edu/", "", true, "Depth", "1")
	send(http.MethodPut, "/calendars/edu/default/e1.ics", event, true, "Content-Type", "text/calendar")
	send("PROPFIND", "/contacts/edu/", "", true, "Depth", "1")
	send(http.MethodPut, "/contacts/edu/contacts/c1.vcf", card, true, "Content-Type", "text/vcard")

	subsonic := "u=edu&p=" + strings.ReplaceAll(password, " ", "%20") + "&v=1.16.1&c=t&id=tr-" + strconv.FormatInt(track.ID, 10)
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/files/notes.txt"},
		{http.MethodGet, "/files/missing.txt"},
		{"PROPFIND", "/files/"},
		{http.MethodGet, "/photos/2024/06/IMG_0001.jpg"},
		{"PROPFIND", "/photos/2024/06/"},
		{http.MethodGet, "/calendars/edu/default/e1.ics"},
		{http.MethodGet, "/contacts/edu/contacts/c1.vcf"},
		{"PROPFIND", "/playlists/"},
		{"PROPFIND", "/trash/"},
		{http.MethodGet, "/rest/stream?" + subsonic},
		{http.MethodGet, "/rest/download?" + subsonic},
	} {
		rec := send(tc.method, tc.target, "", true, "Depth", "1")
		got := rec.Header().Get("Cache-Control")
		if !strings.Contains(got, "private") && !strings.Contains(got, "no-store") {
			t.Errorf("%s %s = %d with Cache-Control %q, want private or no-store", tc.method, tc.target, rec.Code, got)
		}
	}

	// Without a password: refused, and refused privately too.
	if got := send(http.MethodGet, "/files/notes.txt", "", false).Header().Get("Cache-Control"); !strings.Contains(got, "private") {
		t.Errorf("a refusal is cached as %q", got)
	}
	// And what is public on purpose stays public.
	for _, target := range []string{"/robots.txt", "/manifest.webmanifest"} {
		if got := send(http.MethodGet, target, "", false).Header().Get("Cache-Control"); !strings.HasPrefix(got, "public") {
			t.Errorf("%s is cached as %q, want public", target, got)
		}
	}
}
