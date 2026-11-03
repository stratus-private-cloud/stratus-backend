package web_test

import (
	"net/http"
	"strings"
	"testing"
)

const backupModule = "/backup/js/" + version + "/backup.js"

// TestTheBackupPageIsBehindTheSession and loads its module, with both halves
// a browser without it is shown instead: the sentence for no JavaScript and
// the one for no folder access, which the script chooses between.
func TestTheBackupPageIsBehindTheSession(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)

	if rec := get(t, h, "/backup/"); rec.Code != http.StatusSeeOther {
		t.Errorf("GET /backup/ with no session = %d, want 303 to the login form", rec.Code)
	}

	rec := get(t, h, "/backup/", signIn(t, h))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /backup/ = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<script type="module" src="` + backupModule + `"></script>`,
		"<noscript>",
		"data-backup-unsupported",
		`href="/backup/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the backup page has no %s:\n%s", want, body)
		}
	}
}

// TestTheBackupModulesAreOnlyOnTheirPage: the rule #337 was decided under is
// that this page's script stays on this page, so no other page loads it and
// the shell every page installs does not carry it.
func TestTheBackupModulesAreOnlyOnTheirPage(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)
	cookie := signIn(t, h)

	if body := get(t, h, "/status", cookie).Body.String(); strings.Contains(body, "/backup/js/") {
		t.Errorf("another page loads the backup's modules:\n%s", body)
	}
	if body := get(t, h, "/sw.js").Body.String(); strings.Contains(body, "/backup/js/") {
		t.Errorf("the root worker precaches the backup's modules:\n%s", body)
	}
}

// TestTheBackupWorkerIsTheRootOnesWithItsOwnList: served from /backup/ so it
// controls that and nothing else, in front of the session so it registers,
// under a prefix of its own so the two do not delete each other's caches, and
// with a shell every address of which answers with no session.
func TestTheBackupWorkerIsTheRootOnesWithItsOwnList(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)

	rec := get(t, h, "/backup/sw.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /backup/sw.js with no session = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `const PREFIX = "backup:"`) {
		t.Errorf("the backup worker shares the root's cache names:\n%s", body)
	}

	found := shellList.FindStringSubmatch(body)
	if found == nil {
		t.Fatalf("the backup worker was served with no shell:\n%s", body)
	}
	if !strings.Contains(found[1], backupModule) {
		t.Errorf("the backup worker does not precache the page's module: %s", found[1])
	}
	for _, target := range strings.Split(strings.Trim(found[1], "[]"), ",") {
		target = strings.Trim(strings.TrimSpace(target), `"`)
		if rec := get(t, h, target); rec.Code != http.StatusOK {
			t.Errorf("GET %s with no session = %d, and the worker will not install without it", target, rec.Code)
		}
	}
}

// TestABackupModuleIsThisBuildsOrNothing: the address carries the build and
// the answer is immutable, so another build's address answered with these
// bytes would keep them in a browser for a year.
func TestABackupModuleIsThisBuildsOrNothing(t *testing.T) {
	t.Parallel()
	h := newHandler(t, nil)

	rec := get(t, h, backupModule)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", backupModule, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("Content-Type = %q, want a script, or a module is refused", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", got)
	}

	for _, target := range []string{
		"/backup/js/0.0.1/backup.js",
		"/backup/js/" + version + "/missing.js",
		"/backup/js/" + version + "/..%2Fbackup.go",
	} {
		if rec := get(t, h, target); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
	}
}
