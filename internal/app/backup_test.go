package app_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestTheBackupPagesRequestsAreAnswered is the server's half of the backup
// page's contract (#337): the requests internal/web/backup/remote.js sends,
// in its order, with the session and the headers a fetch() from the page
// carries. The script's own tests answer these with a stand-in, so this is
// where a change to the server that the script relies on is caught.
func TestTheBackupPagesRequestsAreAnswered(t *testing.T) {
	t.Parallel()
	const password = "an example password"
	base, stop := liveServer(t, map[string]string{
		"STRATUS_USERNAME": "edu",
		"STRATUS_PASSWORD": password,
	})
	defer stop()

	form := url.Values{"username": {"edu"}, "password": {password}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = login.Body.Close()
	var cookie *http.Cookie
	for _, c := range login.Cookies() {
		if c.Name == "stratus_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("signing in set no session cookie")
	}

	type reply struct {
		code   int
		header http.Header
		body   string
	}
	fetch := func(method, target, body string, headers map[string]string) reply {
		t.Helper()
		if !strings.HasPrefix(target, "http") {
			target = base + target
		}
		r, rerr := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
		if rerr != nil {
			t.Fatal(rerr)
		}
		r.AddCookie(cookie)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Mode", "cors")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		resp, rerr := http.DefaultTransport.RoundTrip(r)
		if rerr != nil {
			t.Fatal(rerr)
		}
		defer func() { _ = resp.Body.Close() }()
		read, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			t.Fatal(rerr)
		}
		return reply{resp.StatusCode, resp.Header, string(read)}
	}
	expect := func(what string, got reply, want int) {
		t.Helper()
		if got.code != want {
			t.Fatalf("%s = %d, want %d: %s", what, got.code, want, got.body)
		}
	}
	depth1 := map[string]string{"Depth": "1"}
	tusHead := map[string]string{"Tus-Resumable": "1.0.0"}

	expect("listing a folder that is not there", fetch("PROPFIND", "/files/desktop_backup/Pictures/", "", depth1), http.StatusNotFound)
	expect("making a folder whose parent is not there", fetch("MKCOL", "/files/desktop_backup/Pictures/", "", nil), http.StatusConflict)
	expect("making the parent", fetch("MKCOL", "/files/desktop_backup/", "", nil), http.StatusCreated)
	expect("making the folder", fetch("MKCOL", "/files/desktop_backup/Pictures/", "", nil), http.StatusCreated)
	expect("making it again", fetch("MKCOL", "/files/desktop_backup/Pictures/", "", nil), http.StatusMethodNotAllowed)

	never := map[string]string{"If-None-Match": "*", "Content-Type": "image/jpeg"}
	expect("a PUT that never replaces", fetch(http.MethodPut, "/files/desktop_backup/Pictures/caf%C3%A9%20%231.jpg", "abc", never), http.StatusCreated)
	expect("the same PUT over it", fetch(http.MethodPut, "/files/desktop_backup/Pictures/caf%C3%A9%20%231.jpg", "abcd", never), http.StatusPreconditionFailed)

	created := fetch(http.MethodPost, "/tus/", "", map[string]string{
		"Tus-Resumable":   "1.0.0",
		"Upload-Length":   "5",
		"Upload-Metadata": "filename " + base64.StdEncoding.EncodeToString([]byte("files/desktop_backup/Pictures/clip.mp4")),
	})
	expect("starting a tus upload", created, http.StatusCreated)
	upload := created.header.Get("Location")
	head := fetch(http.MethodHead, upload, "", tusHead)
	expect("asking where it is", head, http.StatusOK)
	if got := head.header.Get("Upload-Offset"); got != "0" {
		t.Errorf("Upload-Offset of a new upload = %q, want 0", got)
	}
	expect("sending it", fetch(http.MethodPatch, upload, "hello", map[string]string{
		"Tus-Resumable": "1.0.0",
		"Upload-Offset": "0",
		"Content-Type":  "application/offset+octet-stream",
	}), http.StatusNoContent)

	listing := fetch("PROPFIND", "/files/desktop_backup/Pictures/", "", depth1)
	expect("listing the folder", listing, http.StatusMultiStatus)
	for _, want := range []string{"caf%C3%A9%20%231.jpg", "clip.mp4", "<D:getcontentlength>3</D:getcontentlength>", "<D:getcontentlength>5</D:getcontentlength>"} {
		if !strings.Contains(listing.body, want) {
			t.Errorf("the listing the script reads has no %s:\n%s", want, listing.body)
		}
	}
}
