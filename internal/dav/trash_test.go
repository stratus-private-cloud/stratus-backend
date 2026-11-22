package dav_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/dav"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/files"
)

const trashPrefix = "/trash/"

// trashServer is the mount over the real file layer, so that what it lists is
// what deleting actually wrote.
func trashServer(t *testing.T) (http.Handler, *files.Service, db.Store) {
	t.Helper()
	svc, meta := service(t)
	return withUser(dav.Trash(trashPrefix, "/files/", svc), "edu"), svc, meta
}

func put(t *testing.T, svc *files.Service, path, body string) {
	t.Helper()
	if _, err := svc.Write(t.Context(), "edu", path, strings.NewReader(body),
		int64(len(body)), "text/plain"); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, svc *files.Service, path string) {
	t.Helper()
	if err := svc.Remove(t.Context(), "edu", path); err != nil {
		t.Fatal(err)
	}
}

// TestTheTrashIsACollectionOfDeletions is #323: a client that mounts this
// server sees what was deleted, one collection per accident, with the tree
// inside it exactly as it was.
func TestTheTrashIsACollectionOfDeletions(t *testing.T) {
	t.Parallel()
	h, svc, _ := trashServer(t)
	if _, err := svc.Mkdir(t.Context(), "edu", "album"); err != nil {
		t.Fatal(err)
	}
	put(t, svc, "album/one.txt", "one")
	put(t, svc, "album/two.txt", "two")
	put(t, svc, "notes.txt", "notes")
	remove(t, svc, "album")
	remove(t, svc, "notes.txt")

	// A deletion of a folder is a collection; a deletion of a file is the file.
	wantHrefs(t, hrefs(t, do(t, h, "PROPFIND", trashPrefix, "", "Depth", "1")),
		"/trash/", "/trash/album/", "/trash/notes.txt")

	// And the tree under it is the tree that was deleted, under its own names.
	wantHrefs(t, hrefs(t, do(t, h, "PROPFIND", "/trash/album", "", "Depth", "1")),
		"/trash/album/", "/trash/album/one.txt", "/trash/album/two.txt")

	// What the rows say about a deleted file is what the file said: nothing
	// about it changed when the row moved tables.
	props := propsOf(t, do(t, h, "PROPFIND", "/trash/album", "", "Depth", "1").Body.String())
	one := props["/trash/album/one.txt"]
	if one["getcontentlength"] != strconv.Itoa(len("one")) {
		t.Errorf("getcontentlength = %q, want 3", one["getcontentlength"])
	}
	if one["getcontenttype"] != "text/plain" {
		t.Errorf("getcontenttype = %q", one["getcontenttype"])
	}
	if one["getetag"] == "" {
		t.Error("a deleted file has no validator")
	}
}

// TestTwoDeletionsOfOneName: deleting "album" twice is what a trash is for,
// and the older keeps the name so that today's accident does not rename last
// week's under somebody reading it.
func TestTwoDeletionsOfOneName(t *testing.T) {
	t.Parallel()
	h, svc, _ := trashServer(t)
	for range 3 {
		put(t, svc, "album", "whatever")
		remove(t, svc, "album")
	}

	wantHrefs(t, hrefs(t, do(t, h, "PROPFIND", trashPrefix, "", "Depth", "1")),
		"/trash/", "/trash/album", "/trash/album (2)", "/trash/album (3)")
}

// TestTheSweepsFindsAreNotInTheCollection: a blob nobody can account for
// (#276) has no path, so there is nothing to call it here. It stays on the
// page, which can say so in a sentence.
func TestTheSweepsFindsAreNotInTheCollection(t *testing.T) {
	t.Parallel()
	h, svc, meta := trashServer(t)
	put(t, svc, "notes.txt", "notes")
	remove(t, svc, "notes.txt")
	// What the sweep parks: a key, a size, and no owner and no path, because
	// there was none to record.
	if err := meta.Trash(t.Context(), "swept", []db.File{{
		BlobKey: "an orphan", Size: 11, MTime: time.Now().UTC().Truncate(db.TimePrecision),
	}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	// It belongs to nobody, and this tree is always somebody's.
	wantHrefs(t, hrefs(t, do(t, h, "PROPFIND", trashPrefix, "", "Depth", "1")),
		"/trash/", "/trash/notes.txt")
}

// TestTheTrashRefusesEveryWrite but the two it has a meaning for, a restore
// and a destruction, which have tests of their own.
func TestTheTrashRefusesEveryWrite(t *testing.T) {
	t.Parallel()
	h, svc, _ := trashServer(t)
	put(t, svc, "notes.txt", "notes")
	remove(t, svc, "notes.txt")

	for _, m := range []string{http.MethodPut, "MKCOL", "COPY", "PROPPATCH", "LOCK"} {
		rec := do(t, h, m, "/trash/notes.txt", "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405", m, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got == "" {
			t.Errorf("%s: no Allow header on the refusal", m)
		}
	}

	// Class 1 and no LOCK: there is nothing here to hold.
	opts := do(t, h, http.MethodOptions, trashPrefix, "")
	if got := opts.Header().Get("DAV"); got != "1" {
		t.Errorf("DAV = %q, want 1", got)
	}
	if got := opts.Header().Get("Allow"); strings.Contains(got, "LOCK") {
		t.Errorf("Allow = %q, a read-only mount offers no locks", got)
	}
}

// TestNothingIsAtAnInventedName: the trash answers for what was deleted and
// not for everything asked of it.
func TestNothingIsAtAnInventedName(t *testing.T) {
	t.Parallel()
	h, svc, _ := trashServer(t)
	put(t, svc, "notes.txt", "notes")
	remove(t, svc, "notes.txt")

	for _, target := range []string{"/trash/album", "/trash/notes.txt/deeper", "/trash/album%20(2)"} {
		if got := do(t, h, "PROPFIND", target, "", "Depth", "1").Code; got != http.StatusNotFound {
			t.Errorf("PROPFIND %s = %d, want 404", target, got)
		}
	}
}

// TestTheTrashRefusesInfiniteDepth, like every other collection here: a client
// that meant the whole tree is told, rather than quietly given one level.
func TestTheTrashRefusesInfiniteDepth(t *testing.T) {
	t.Parallel()
	h, _, _ := trashServer(t)
	if got := do(t, h, "PROPFIND", trashPrefix, "", "Depth", "infinity").Code; got != http.StatusForbidden {
		t.Errorf("Depth: infinity = %d, want 403", got)
	}
}

// TestADeletedFileSaysNothingAboutWhereItWas: its path in the tree is no
// address any more, and a client that cast it would hand a television a 404
// (#431).
func TestADeletedFileSaysNothingAboutWhereItWas(t *testing.T) {
	t.Parallel()
	h, svc, _ := trashServer(t)
	put(t, svc, "notes.txt", "notes")
	remove(t, svc, "notes.txt")

	if got := sourcesOf(t, do(t, h, "PROPFIND", trashPrefix, "", "Depth", "1").Body.String()); len(got) != 0 {
		t.Errorf("sources = %v, want none", got)
	}
}

// TestAMoveOutOfTheTrashRestoresIt is stratus-app#179: the page's Restore as
// the verb that already means it, to where the client says.
func TestAMoveOutOfTheTrashRestoresIt(t *testing.T) {
	t.Parallel()
	h, svc, meta := trashServer(t)
	if _, err := svc.Mkdir(t.Context(), "edu", "album"); err != nil {
		t.Fatal(err)
	}
	put(t, svc, "album/one.txt", "one")
	remove(t, svc, "album")

	if got := do(t, h, "MOVE", "/trash/album", "", "Destination", "http://example.com/files/album").Code; got != http.StatusCreated {
		t.Fatalf("MOVE = %d, want 201", got)
	}
	if _, err := meta.FileByPath(t.Context(), "edu", "album/one.txt"); err != nil {
		t.Errorf("not restored: %v", err)
	}
	wantHrefs(t, hrefs(t, do(t, h, "PROPFIND", trashPrefix, "", "Depth", "1")), "/trash/")
}

// TestAMoveNeverReplaces: what has the name now is not a mistake, so a taken
// destination is the client's to number around, Overwrite or not.
func TestAMoveNeverReplaces(t *testing.T) {
	t.Parallel()
	h, svc, meta := trashServer(t)
	put(t, svc, "notes.txt", "old")
	remove(t, svc, "notes.txt")
	put(t, svc, "notes.txt", "new")

	for _, overwrite := range []string{"F", "T"} {
		rec := do(t, h, "MOVE", "/trash/notes.txt", "", "Destination", "/files/notes.txt", "Overwrite", overwrite)
		if rec.Code != http.StatusPreconditionFailed {
			t.Errorf("Overwrite %s: MOVE = %d, want 412", overwrite, rec.Code)
		}
	}
	if got := do(t, h, "MOVE", "/trash/notes.txt", "", "Destination", "/files/a/notes%20(2).txt").Code; got != http.StatusCreated {
		t.Fatalf("MOVE beside = %d, want 201", got)
	}
	if _, err := meta.FileByPath(t.Context(), "edu", "a/notes (2).txt"); err != nil {
		t.Errorf("not restored beside, with its folder made on the way: %v", err)
	}
}

// TestADeleteInTheTrashDestroysIt, the page's Destroy.
func TestADeleteInTheTrashDestroysIt(t *testing.T) {
	t.Parallel()
	h, svc, _ := trashServer(t)
	put(t, svc, "notes.txt", "notes")
	remove(t, svc, "notes.txt")

	if got := do(t, h, http.MethodDelete, "/trash/notes.txt", "").Code; got != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204", got)
	}
	wantHrefs(t, hrefs(t, do(t, h, "PROPFIND", trashPrefix, "", "Depth", "1")), "/trash/")
	if got := do(t, h, http.MethodDelete, "/trash/notes.txt", "").Code; got != http.StatusNotFound {
		t.Errorf("DELETE again = %d, want 404", got)
	}
}

// TestOnlyAWholeDeletionIsWritten: a batch is the one shape the port restores
// and destroys, and anything outside the tree has nowhere to go.
func TestOnlyAWholeDeletionIsWritten(t *testing.T) {
	t.Parallel()
	h, svc, _ := trashServer(t)
	if _, err := svc.Mkdir(t.Context(), "edu", "album"); err != nil {
		t.Fatal(err)
	}
	put(t, svc, "album/one.txt", "one")
	remove(t, svc, "album")

	for _, target := range []string{"/trash/", "/trash/album/one.txt"} {
		if got := do(t, h, http.MethodDelete, target, "").Code; got != http.StatusForbidden {
			t.Errorf("DELETE %s = %d, want 403", target, got)
		}
	}
	for _, dest := range []string{"/music/album", "/files/", ""} {
		if got := do(t, h, "MOVE", "/trash/album", "", "Destination", dest).Code; got != http.StatusBadGateway {
			t.Errorf("MOVE to %q = %d, want 502", dest, got)
		}
	}
	if got := do(t, h, http.MethodOptions, trashPrefix, "").Header().Get("Allow"); !strings.Contains(got, "MOVE") ||
		!strings.Contains(got, "DELETE") {
		t.Errorf("Allow = %q, want MOVE and DELETE offered", got)
	}
}

// TestADeletionSaysWhereItWasTakenFrom, which is where a client restores it
// to; what is inside it goes back with it and says nothing.
func TestADeletionSaysWhereItWasTakenFrom(t *testing.T) {
	t.Parallel()
	h, svc, _ := trashServer(t)
	if _, err := svc.Mkdir(t.Context(), "edu", "my album"); err != nil {
		t.Fatal(err)
	}
	put(t, svc, "my album/one.txt", "one")
	remove(t, svc, "my album")

	body := do(t, h, "PROPFIND", trashPrefix, "", "Depth", "1").Body.String()
	if !strings.Contains(body, "deleted-from") || !strings.Contains(body, `/files/my%20album</D:href></deleted-from>`) {
		t.Errorf("no deleted-from for the deletion in:\n%s", body)
	}
	inside := do(t, h, "PROPFIND", "/trash/my%20album/", "", "Depth", "1").Body.String()
	if strings.Count(inside, "<deleted-from") != 1 {
		t.Errorf("want deleted-from on the deletion alone, in:\n%s", inside)
	}
}
