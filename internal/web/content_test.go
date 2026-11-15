package web_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/auth"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/dbtest"
	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/web"
)

// contents answers every query with the photographs it was given, in that
// order, and counts what it was told to.
type contents struct {
	hits   []embed.Hit
	counts db.EmbeddingCounts
	asked  []string
	state  string
	fail   error
	likeOf []int64
}

func (c *contents) State() string { return c.state }

func (c *contents) Counts(context.Context) (db.EmbeddingCounts, error) { return c.counts, c.fail }

func (c *contents) Similar(_ context.Context, id int64, limit int) ([]embed.Hit, error) {
	c.likeOf = append(c.likeOf, id)
	if c.fail != nil {
		return nil, c.fail
	}
	return c.hits[:min(limit, len(c.hits))], nil
}

func (c *contents) Search(_ context.Context, q string, limit int) ([]embed.Hit, error) {
	c.asked = append(c.asked, q)
	if c.fail != nil {
		return nil, c.fail
	}
	return c.hits[:min(limit, len(c.hits))], nil
}

func browserWithContents(t *testing.T, c web.Contents, on bool, fail ...string) (http.Handler, *http.Cookie, func(name string) db.File) {
	t.Helper()
	s, thumbs, meta := pieces(t)
	creds := credentials()
	ix := indexing(meta)
	ix.Contents, ix.Semantic = c, on
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

func TestASearchFindsPhotographsByWhatIsInThem(t *testing.T) {
	t.Parallel()
	c := &contents{}
	h, cookie, photo := browserWithContents(t, c, true)
	dog := photo("IMG_0001.jpg")
	beach := photo("IMG_0002.jpg")
	photo("IMG_0003.jpg")
	c.hits = []embed.Hit{{FileID: beach.ID, Score: 0.12}, {FileID: dog.ID, Score: 0.09}, {FileID: 99999, Score: 0.08}}

	body := get(t, h, "/search?q=perro+en+la+playa", cookie).Body.String()
	has(t, body, "In the photos", "IMG_0002.jpg", "IMG_0001.jpg")
	if strings.Contains(body, "IMG_0003.jpg") {
		t.Error("a photograph the search did not find is in the results")
	}
	if strings.Index(body, "IMG_0002.jpg") > strings.Index(body, "IMG_0001.jpg") {
		t.Error("the closest photograph is not first")
	}
	if len(c.asked) != 1 || c.asked[0] != "perro en la playa" {
		t.Errorf("the search was asked %q", c.asked)
	}

	alone := get(t, h, "/search?q=perro&in=content", cookie)
	if alone.Code != http.StatusOK {
		t.Fatalf("the page narrowed to content = %d", alone.Code)
	}
	has(t, alone.Body.String(), "In the photos", "IMG_0002.jpg")
}

func TestThereIsNoContentBucketWhileItIsOff(t *testing.T) {
	t.Parallel()
	c := &contents{}
	h, cookie, photo := browserWithContents(t, c, false)
	c.hits = []embed.Hit{{FileID: photo("IMG_0001.jpg").ID, Score: 0.2}}

	body := get(t, h, "/search?q=perro", cookie).Body.String()
	if strings.Contains(body, "In the photos") || len(c.asked) != 0 {
		t.Errorf("a search by content that is off was asked %v:\n%s", c.asked, body)
	}
}

func TestStatusSaysHowMuchContentIsRead(t *testing.T) {
	t.Parallel()
	c := &contents{counts: db.EmbeddingCounts{Files: 10, Made: 4, Refused: 1}}
	h, cookie, _ := browserWithContents(t, c, true)
	has(t, get(t, h, "/status", cookie).Body.String(), "Content read", "4 of 10", "Content unreadable")
	c.state = "fetching the model"
	has(t, get(t, h, "/status", cookie).Body.String(), "4 of 10", "fetching the model")

	missing, cookie2, _ := browserWithContents(t, nil, true)
	has(t, get(t, missing, "/status", cookie2).Body.String(), "Content read", "<code>stratus-embed</code> or its model is not installed")

	off, cookie3, _ := browserWithContents(t, nil, false)
	if body := get(t, off, "/status", cookie3).Body.String(); strings.Contains(body, "Content read") {
		t.Error("the page reports a search by content that is off and not installed")
	}
}

// TestASearchByContentThatFailsLeavesTheRestOfThePage: the bucket is empty and
// the page is still an answer, as the calendar's is when it fails.
func TestASearchByContentThatFailsLeavesTheRestOfThePage(t *testing.T) {
	t.Parallel()
	c := &contents{fail: errors.New("the helper is unavailable")}
	h, cookie, photo := browserWithContents(t, c, true)
	photo("IMG_0001.jpg")

	rec := get(t, h, "/search?q=perro", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("a search whose content half failed = %d", rec.Code)
	}
	has(t, rec.Body.String(), "In the photos", "Nothing in a photograph looks like that.")

	if body := get(t, h, "/status", cookie).Body.String(); strings.Contains(body, "Content read") {
		t.Error("the status page reports counts it could not read")
	}
}

// TestMoreThanARowOfContentIsALinkToTheRest: twenty-four beside the other
// buckets, and the rest on the page narrowed to them.
func TestMoreThanARowOfContentIsALinkToTheRest(t *testing.T) {
	t.Parallel()
	c := &contents{}
	h, cookie, photo := browserWithContents(t, c, true)
	for i := range 30 {
		c.hits = append(c.hits, embed.Hit{FileID: photo(fmt.Sprintf("IMG_%04d.jpg", i)).ID, Score: 0.1})
	}

	body := get(t, h, "/search?q=perro", cookie).Body.String()
	has(t, body, "More photos like this", "in=content")
	if strings.Contains(body, "IMG_0024.jpg") {
		t.Error("more than a row of content is on the page with the other buckets")
	}
	has(t, get(t, h, "/search?q=perro&in=content", cookie).Body.String(), "IMG_0029.jpg")
}

// TestAPhotographsDetailsShowTheOnesLikeIt (#514): the tiles, closest first,
// in the dialog and on the page; nothing for a file that is not a photograph,
// with the search off, or when it fails.
func TestAPhotographsDetailsShowTheOnesLikeIt(t *testing.T) {
	t.Parallel()
	c := &contents{}
	h, cookie, photo := browserWithContents(t, c, true)
	me := photo("IMG_0001.jpg")
	near := photo("IMG_0002.jpg")
	far := photo("IMG_0003.jpg")
	c.hits = []embed.Hit{{FileID: near.ID, Score: 0.95}, {FileID: far.ID, Score: 0.82}, {FileID: 99999, Score: 0.81}}

	page := get(t, h, "/info/IMG_0001.jpg", cookie).Body.String()
	has(t, page, "Like this", "IMG_0002.jpg", "IMG_0003.jpg")
	if strings.Index(page, "IMG_0002.jpg") > strings.Index(page, "IMG_0003.jpg") {
		t.Error("the closest photograph is not first")
	}
	if len(c.likeOf) != 1 || c.likeOf[0] != me.ID {
		t.Errorf("Similar was asked about %v, want the photograph itself", c.likeOf)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/info/IMG_0001.jpg", nil)
	req.AddCookie(cookie)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	has(t, rec.Body.String(), "<dialog", "Like this", "IMG_0002.jpg")

	// Asked by the name, as every surface here asks what a file is.
	photo("notes.txt")
	c.likeOf = nil
	if body := get(t, h, "/info/notes.txt", cookie).Body.String(); strings.Contains(body, "Like this") || len(c.likeOf) != 0 {
		t.Error("a file that is not a photograph was asked about")
	}

	c.fail = errors.New("the index will not load")
	rec2 := get(t, h, "/info/IMG_0001.jpg", cookie)
	if rec2.Code != http.StatusOK || strings.Contains(rec2.Body.String(), "Like this") {
		t.Errorf("details whose likeness failed = %d, showing the section anyway", rec2.Code)
	}

	off, cookieOff, photoOff := browserWithContents(t, &contents{hits: c.hits}, false)
	photoOff("IMG_0001.jpg")
	if body := get(t, off, "/info/IMG_0001.jpg", cookieOff).Body.String(); strings.Contains(body, "Like this") {
		t.Error("the details show likeness with the search by content off")
	}
}

// TestPhotographsThatCannotBeReadBackAreLeftOut: the ids came back and the rows
// did not, and both the search and the details are still pages.
func TestPhotographsThatCannotBeReadBackAreLeftOut(t *testing.T) {
	t.Parallel()
	c := &contents{}
	h, cookie, photo := browserWithContents(t, c, true, "FilesByID")
	near := photo("IMG_0002.jpg")
	photo("IMG_0001.jpg")
	c.hits = []embed.Hit{{FileID: near.ID, Score: 0.9}}

	search := get(t, h, "/search?q=perro&in=content", cookie)
	if search.Code != http.StatusOK || strings.Contains(search.Body.String(), "IMG_0002.jpg") {
		t.Errorf("a search whose rows failed = %d", search.Code)
	}
	details := get(t, h, "/info/IMG_0001.jpg", cookie)
	if details.Code != http.StatusOK || strings.Contains(details.Body.String(), "Like this") {
		t.Errorf("details whose rows failed = %d", details.Code)
	}
}
