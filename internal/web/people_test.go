package web_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/auth"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/dbtest"
	"github.com/stratus-private-cloud/stratus-backend/internal/web"
)

// people is the faces in memory: who there is, and which face of which
// photograph is whose.
type people struct {
	everybody []db.Person
	faces     []db.Face
	state     string
	fail      error
	// failing is the one call that answers broken, by name.
	failing string
}

var errBroken = errors.New("broken")

func (p *people) Counts(context.Context) (db.FaceCounts, error) {
	return db.FaceCounts{Files: 10, Scanned: 4, Faces: int64(len(p.faces)), People: int64(len(p.everybody))}, p.fail
}

func (p *people) State() string { return p.state }

func (p *people) People(context.Context) ([]db.Person, error) {
	if p.fail != nil {
		return nil, p.fail
	}
	if p.failing == "People" {
		return nil, errBroken
	}
	out := make([]db.Person, 0, len(p.everybody))
	for _, person := range p.everybody {
		person.Faces = 0
		for _, f := range p.faces {
			if f.Person == person.ID {
				person.Faces++
				person.Cover = f.ID
			}
		}
		if person.Faces > 0 {
			out = append(out, person)
		}
	}
	return out, nil
}

func (p *people) PersonFaces(_ context.Context, person int64, after db.FaceCursor, limit int) ([]db.Face, error) {
	if p.failing == "PersonFaces" {
		return nil, errBroken
	}
	var out []db.Face
	for _, f := range p.faces {
		if f.Person == person && (after.ID == 0 || f.ID < after.ID) && len(out) < limit {
			out = append(out, f)
		}
	}
	return out, nil
}

func (p *people) FacesOf(_ context.Context, file db.File) ([]db.Face, error) {
	var out []db.Face
	for _, f := range p.faces {
		if f.FileID == file.ID {
			out = append(out, f)
		}
	}
	return out, p.fail
}

func (p *people) Rename(_ context.Context, person int64, name string) error {
	if strings.Contains(name, "/") {
		return errors.New("a name cannot have a slash in it")
	}
	for i := range p.everybody {
		if p.everybody[i].ID == person {
			p.everybody[i].Name = strings.TrimSpace(name)
		}
	}
	return nil
}

func (p *people) Merge(_ context.Context, from, into int64) error {
	if p.failing == "Merge" {
		return errBroken
	}
	for i := range p.faces {
		if p.faces[i].Person == from {
			p.faces[i].Person = into
		}
	}
	return nil
}

func (p *people) Remove(_ context.Context, person int64, faces []int64) error {
	if p.failing == "Remove" {
		return errBroken
	}
	for i := range p.faces {
		if p.faces[i].Person == person && slices.Contains(faces, p.faces[i].ID) {
			p.faces[i].Person = 0
		}
	}
	return nil
}

func (p *people) Crop(_ context.Context, face int64) (io.ReadCloser, int64, error) {
	if p.failing == "Crop" {
		return nil, 0, errBroken
	}
	for _, f := range p.faces {
		if f.ID == face {
			return io.NopCloser(bytes.NewReader([]byte("jpeg"))), 4, nil
		}
	}
	return nil, 0, db.ErrNotFound
}

func browserWithPeople(t *testing.T, p web.People, on bool, fail ...string) (http.Handler, *http.Cookie, func(name string) db.File) {
	t.Helper()
	s, thumbs, meta := pieces(t)
	creds := credentials()
	ix := indexing(meta)
	ix.People, ix.Faces = p, on
	var index web.Index = meta
	for _, method := range fail {
		index = dbtest.FailOn(t, meta, method)
	}
	h := web.Handler(web.Deps{
		Version: version, BuildDate: buildDate, Verifier: creds,
		Sessions: auth.NewSessions(creds, auth.DefaultSessionTTL), Shares: auth.NewShares(creds),
		Files: s, Thumbs: thumbs, Index: index, Playlists: playlistsOf(meta), Indexing: ix,
		TrashRetention: aMonth,
	})
	photo := func(name string) db.File {
		return addPhoto(t, s, meta, name, time.Date(2024, 8, 1, 9, 0, 0, 0, time.UTC), "")
	}
	return h, signIn(t, h), photo
}

// withFaces is two people, one named, with three photographs between them.
func withFaces(t *testing.T, on bool) (*people, http.Handler, *http.Cookie, []db.File) {
	t.Helper()
	p := &people{everybody: []db.Person{{ID: 1}, {ID: 2, Name: "Ana"}}}
	h, cookie, photo := browserWithPeople(t, p, on)
	var photos []db.File
	for i := range 3 {
		f := photo(fmt.Sprintf("IMG_%04d.jpg", i))
		photos = append(photos, f)
		p.faces = append(p.faces, db.Face{ID: int64(10 + i), FileID: f.ID, BlobKey: f.BlobKey, Person: int64(1 + i%2)})
	}
	return p, h, cookie, photos
}

func TestThePeoplePageShowsEverybodyByTheirName(t *testing.T) {
	t.Parallel()
	_, h, cookie, _ := withFaces(t, true)

	body := get(t, h, "/people/", cookie).Body.String()
	has(t, body, `href="/people/Person%201"`, `href="/people/Ana"`, `src="/people/Ana/?cover&amp;face=`, `href="/people/">People</a>`)

	page := get(t, h, "/people/Person%201", cookie)
	if page.Code != http.StatusOK {
		t.Fatalf("a person's page = %d", page.Code)
	}
	has(t, page.Body.String(), "Who is this?", "IMG_0000.jpg", "IMG_0002.jpg", `<option value="Ana">Ana</option>`)
	if strings.Contains(page.Body.String(), "IMG_0001.jpg") {
		t.Error("somebody else's photograph is on a person's page")
	}

	if rec := get(t, h, "/people/Nobody", cookie); rec.Code != http.StatusNotFound {
		t.Errorf("a person who is not there = %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/people/Ana?after=x", cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("a cursor that is not one = %d, want 400", rec.Code)
	}

	crop := get(t, h, "/face/10", cookie)
	if crop.Code != http.StatusOK || crop.Header().Get("Content-Type") != "image/jpeg" ||
		!strings.Contains(crop.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("a face's picture = %d %v", crop.Code, crop.Header())
	}
	for _, missing := range []string{"/face/99", "/face/x"} {
		if rec := get(t, h, missing, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", missing, rec.Code)
		}
	}
}

// TestAPersonsCoverIsTheirFolderAsked: the face a person's tile shows, at
// their own address, which a WebDAV client reaches with Basic and no cookie.
func TestAPersonsCoverIsTheirFolderAsked(t *testing.T) {
	t.Parallel()
	p, h, _, _ := withFaces(t, true)

	ask := func(target, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		req.SetBasicAuth(username, examplePassword)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := ask("/people/Ana/?cover=300", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.String() != "jpeg" {
		t.Fatalf("a person's cover = %d %v", rec.Code, rec.Header())
	}
	etag := rec.Header().Get("ETag")
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=259200" || etag == "" {
		t.Errorf("a cover a merge can change is cached as %q with ETag %q, want days and privately", got, etag)
	}
	// The address the page draws names the face, so it is kept for good --
	// and one naming a face that is no longer the cover is not.
	face := strings.TrimSuffix(strings.TrimPrefix(etag, `"face-`), `"`)
	if got := ask("/people/Ana/?cover&face="+face, "").Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Errorf("the cover at the address naming its face is cached as %q", got)
	}
	if got := ask("/people/Ana/?cover&face=0", "").Header().Get("Cache-Control"); strings.Contains(got, "immutable") {
		t.Errorf("the cover at an address naming another face is cached as %q", got)
	}
	if again := ask("/people/Ana/?cover", etag); again.Code != http.StatusNotModified {
		t.Errorf("asked again with its ETag = %d, want 304", again.Code)
	}

	// Ana's faces become Person 1's, whose cover is now another face.
	if err := p.Merge(t.Context(), 2, 1); err != nil {
		t.Fatal(err)
	}
	if moved := ask("/people/Person%201/?cover", etag); moved.Code != http.StatusOK || moved.Header().Get("ETag") == etag {
		t.Errorf("a merged person's cover = %d with ETag %q, the old one was %q", moved.Code, moved.Header().Get("ETag"), etag)
	}
	if gone := ask("/people/Nobody/?cover", ""); gone.Code != http.StatusNotFound {
		t.Errorf("the cover of nobody = %d, want 404", gone.Code)
	}
}

func TestAPersonIsNamedAndMerged(t *testing.T) {
	t.Parallel()
	p, h, cookie, _ := withFaces(t, true)

	rec := post(t, h, "/people/Person%201", url.Values{"do": {"rename"}, "name": {" Bea "}}, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/people/Bea" {
		t.Fatalf("rename = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if bad := post(t, h, "/people/Bea", url.Values{"do": {"rename"}, "name": {"a/b"}}, cookie); bad.Code != http.StatusBadRequest {
		t.Errorf("a name with a slash = %d, want 400", bad.Code)
	}

	rec = post(t, h, "/people/Bea", url.Values{"do": {"merge"}, "into": {"Ana"}}, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/people/Ana" {
		t.Fatalf("merge = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if all, _ := p.People(t.Context()); len(all) != 1 || all[0].Faces != 3 {
		t.Errorf("after a merge: %+v", all)
	}

	for _, form := range []url.Values{{"do": {"merge"}, "into": {"Nobody"}}, {"do": {"dance"}}} {
		if rec := post(t, h, "/people/Ana", form, cookie); rec.Code != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400", form, rec.Code)
		}
	}
	if rec := post(t, h, "/people/Nobody", url.Values{"do": {"rename"}, "name": {"x"}}, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("renaming nobody = %d, want 404", rec.Code)
	}
}

func TestAFaceIsTakenOutOfAPerson(t *testing.T) {
	t.Parallel()
	p, h, cookie, _ := withFaces(t, true)

	page := get(t, h, "/people/Person%201", cookie).Body.String()
	has(t, page, `id="not-them"`, `value="10" form="not-them"`, `value="12" form="not-them"`)

	rec := post(t, h, "/people/Person%201", url.Values{"do": {"remove"}, "face": {"10", "11"}}, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/people/Person%201" {
		t.Fatalf("remove = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if p.faces[0].Person != 0 || p.faces[1].Person != 2 || p.faces[2].Person != 1 {
		t.Errorf("after taking 10 out of Person 1: %+v; 11 is Ana's and stays", p.faces)
	}

	for _, form := range []url.Values{{"do": {"remove"}}, {"do": {"remove"}, "face": {"ten"}}} {
		if rec := post(t, h, "/people/Person%201", form, cookie); rec.Code != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400", form, rec.Code)
		}
	}
	if strings.Contains(get(t, h, "/photos/", cookie).Body.String(), `form="not-them"`) {
		t.Error("the gallery's tiles carry a box for taking a face out")
	}
}

func TestThePhotoViewerSaysWhoIsInIt(t *testing.T) {
	t.Parallel()
	_, h, cookie, photos := withFaces(t, true)
	body := get(t, h, "/photos/?file="+url.QueryEscape(photos[1].Path), cookie)
	if body.Code == http.StatusFound || body.Code == http.StatusSeeOther {
		body = get(t, h, body.Header().Get("Location"), cookie)
	}
	has(t, body.Body.String(), "In this photo", `href="/people/Ana"`)
}

func TestFacesOffShowNobody(t *testing.T) {
	t.Parallel()
	_, h, cookie, photos := withFaces(t, false)

	body := get(t, h, "/people/", cookie).Body.String()
	has(t, body, "Faces are off")
	if strings.Contains(body, `href="/people/">People</a>`) {
		t.Error("the gallery's menu offers people with faces off")
	}
	for _, target := range []string{"/people/Ana", "/face/10"} {
		if rec := get(t, h, target, cookie); rec.Code != http.StatusNotFound {
			t.Errorf("%s with faces off = %d, want 404", target, rec.Code)
		}
	}
	if rec := post(t, h, "/people/Ana", url.Values{"do": {"rename"}, "name": {"x"}}, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("renaming with faces off = %d, want 404", rec.Code)
	}
	viewer := get(t, h, "/photos/?file="+url.QueryEscape(photos[1].Path), cookie)
	if viewer.Code == http.StatusFound || viewer.Code == http.StatusSeeOther {
		viewer = get(t, h, viewer.Header().Get("Location"), cookie)
	}
	if strings.Contains(viewer.Body.String(), "In this photo") {
		t.Error("the viewer says who is in a photograph with faces off")
	}
	if strings.Contains(get(t, h, "/status", cookie).Body.String(), "Faces read") {
		t.Error("the status page counts faces with faces off")
	}
}

func TestFacesOnWithNothingToRunThemSaySo(t *testing.T) {
	t.Parallel()
	h, cookie, _ := browserWithPeople(t, nil, true)
	has(t, get(t, h, "/people/", cookie).Body.String(), "is not installed")
	has(t, get(t, h, "/status", cookie).Body.String(), "Faces read", "<code>STRATUS_FACES</code> is on")
}

func TestTheStatusPageCountsFaces(t *testing.T) {
	t.Parallel()
	p, h, cookie, _ := withFaces(t, true)
	p.state = "fetching the model"
	has(t, get(t, h, "/status", cookie).Body.String(), "Faces read", "4 of 10", "3 faces, 2 people", "fetching the model")

	p.fail = errors.New("the database went away")
	if strings.Contains(get(t, h, "/status", cookie).Body.String(), "Faces read") {
		t.Error("the status page reports counts it could not read")
	}
	if rec := get(t, h, "/people/", cookie); rec.Code != http.StatusInternalServerError {
		t.Errorf("the people page over a failing store = %d, want 500", rec.Code)
	}
}

// TestAPersonsPhotographsArePaged: a page of sixty, and the link to the
// rest, which is the next sixty after the last face shown.
func TestAPersonsPhotographsArePaged(t *testing.T) {
	t.Parallel()
	p := &people{everybody: []db.Person{{ID: 1, Name: "Ana"}}}
	h, cookie, photo := browserWithPeople(t, p, true)
	for i := range 61 {
		f := photo(fmt.Sprintf("IMG_%04d.jpg", i))
		// Newest first, as the store answers: the highest id is the first.
		p.faces = append([]db.Face{{ID: int64(100 + i), FileID: f.ID, BlobKey: f.BlobKey, Person: 1,
			SortAt: time.Date(2024, 1, 1, 0, 0, i, 0, time.UTC)}}, p.faces...)
	}
	// One whose file is gone is left out rather than drawn as a broken link.
	p.faces = append(p.faces, db.Face{ID: 1, FileID: 1 << 40, Person: 1})

	body := get(t, h, "/people/Ana", cookie).Body.String()
	has(t, body, "IMG_0060.jpg", "IMG_0001.jpg", "?after=1704067201000-101")
	if strings.Contains(body, `title="IMG_0000.jpg"`) {
		t.Error("the sixty-first is on the first page")
	}
	has(t, get(t, h, "/people/Ana?after=1704067201000-101", cookie).Body.String(), "IMG_0000.jpg")
	more := htmx(t, h, "/people/Ana?after=1704067201000-101", cookie).Body.String()
	has(t, more, "IMG_0000.jpg")
	if strings.Contains(more, "<html") || strings.Contains(more, "Merge") {
		t.Error("the next faces come with the page around them")
	}
	if !strings.Contains(body, `hx-trigger="revealed"`) {
		t.Error("the first page does not ask for the next as it scrolls")
	}
	for _, bad := range []string{"12", "x-1", "12-x", "12-0"} {
		if rec := get(t, h, "/people/Ana?after="+bad, cookie); rec.Code != http.StatusBadRequest {
			t.Errorf("after=%s = %d, want 400", bad, rec.Code)
		}
	}
}

// TestAViewerWhoseFacesCannotBeReadIsStillTheViewer: who is in it is extra.
func TestAViewerWhoseFacesCannotBeReadIsStillTheViewer(t *testing.T) {
	t.Parallel()
	p, h, cookie, photos := withFaces(t, true)
	p.fail = errors.New("the database went away")
	rec := get(t, h, "/photos/?file="+url.QueryEscape(photos[1].Path), cookie)
	if rec.Code == http.StatusFound || rec.Code == http.StatusSeeOther {
		rec = get(t, h, rec.Header().Get("Location"), cookie)
	}
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "In this photo") {
		t.Errorf("the viewer with faces failing = %d", rec.Code)
	}
}

// TestWhatFailsUnderThePeoplePagesIsAnError: every call those pages make,
// broken in turn, is a 500 and not a page that pretends.
func TestWhatFailsUnderThePeoplePagesIsAnError(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		failing, method, target string
		form                    url.Values
	}{
		{"PersonFaces", http.MethodGet, "/people/Ana", nil},
		{"Crop", http.MethodGet, "/face/10", nil},
		{"People", http.MethodPost, "/people/Ana", url.Values{"do": {"rename"}, "name": {"x"}}},
		{"Merge", http.MethodPost, "/people/Ana", url.Values{"do": {"merge"}, "into": {"Person 1"}}},
		{"Remove", http.MethodPost, "/people/Ana", url.Values{"do": {"remove"}, "face": {"11"}}},
	} {
		p, h, cookie, _ := withFaces(t, true)
		p.failing = c.failing
		var rec *httptest.ResponseRecorder
		if c.method == http.MethodGet {
			rec = get(t, h, c.target, cookie)
		} else {
			rec = post(t, h, c.target, c.form, cookie)
		}
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s broken: %s %s = %d, want 500", c.failing, c.method, c.target, rec.Code)
		}
	}

	p := &people{everybody: []db.Person{{ID: 2, Name: "Ana"}}}
	h, cookie, photo := browserWithPeople(t, p, true, "FilesByID")
	f := photo("IMG_0001.jpg")
	p.faces = []db.Face{{ID: 10, FileID: f.ID, BlobKey: f.BlobKey, Person: 2}}
	if rec := get(t, h, "/people/Ana", cookie); rec.Code != http.StatusInternalServerError {
		t.Errorf("a person's page whose photographs cannot be read = %d, want 500", rec.Code)
	}
}

// TestTheViewerNamesOnlyWhoIsThere: a face under nobody, or under somebody
// the list does not have, is not a name in the viewer; nor is the list it
// could not read.
func TestTheViewerNamesOnlyWhoIsThere(t *testing.T) {
	t.Parallel()
	p, h, cookie, photos := withFaces(t, true)
	p.faces = append(p.faces,
		db.Face{ID: 50, FileID: photos[1].ID, BlobKey: photos[1].BlobKey},
		db.Face{ID: 51, FileID: photos[1].ID, BlobKey: photos[1].BlobKey, Person: 99},
		db.Face{ID: 52, FileID: photos[1].ID, BlobKey: photos[1].BlobKey, Person: 2})
	viewer := func() string {
		rec := get(t, h, "/photos/?file="+url.QueryEscape(photos[1].Path), cookie)
		if rec.Code == http.StatusFound || rec.Code == http.StatusSeeOther {
			rec = get(t, h, rec.Header().Get("Location"), cookie)
		}
		return rec.Body.String()
	}
	body := viewer()
	if n := strings.Count(body, `href="/people/Ana"`); n != 1 {
		t.Errorf("Ana is named %d times in the viewer, want once", n)
	}
	if strings.Contains(body, "/people/Person%2099") {
		t.Error("somebody who is not in the list is named")
	}
	p.failing = "People"
	if strings.Contains(viewer(), "In this photo") {
		t.Error("the viewer names people it could not read")
	}
}

// TestAPhotographInSomebodysFolderIsTheFile: what a WebDAV client GETs of the
// people mount is the browser half's to serve, as under /photos/.
func TestAPhotographInSomebodysFolderIsTheFile(t *testing.T) {
	t.Parallel()
	_, h, cookie, photos := withFaces(t, true)
	rec := get(t, h, "/people/Ana/"+path.Base(photos[1].Path), cookie)
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("a photograph in Ana's folder = %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if rec := get(t, h, "/people/Ana/"+path.Base(photos[0].Path), cookie); rec.Code != http.StatusNotFound {
		t.Errorf("somebody else's photograph in Ana's folder = %d, want 404", rec.Code)
	}

	p := &people{everybody: []db.Person{{ID: 2, Name: "Ana"}}}
	broken, cookie2, photo := browserWithPeople(t, p, true, "FilesByID")
	f := photo("IMG_0001.jpg")
	p.faces = []db.Face{{ID: 10, FileID: f.ID, BlobKey: f.BlobKey, Person: 2}}
	if rec := get(t, broken, "/people/Ana/IMG_0001.jpg", cookie2); rec.Code != http.StatusInternalServerError {
		t.Errorf("a photograph whose row cannot be read = %d, want 500", rec.Code)
	}
}
