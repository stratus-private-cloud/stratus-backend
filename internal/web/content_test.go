package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/auth"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
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
}

func (c *contents) State() string { return c.state }

func (c *contents) Counts(context.Context) (db.EmbeddingCounts, error) { return c.counts, nil }

func (c *contents) Search(_ context.Context, q string, limit int) ([]embed.Hit, error) {
	c.asked = append(c.asked, q)
	return c.hits[:min(limit, len(c.hits))], nil
}

func browserWithContents(t *testing.T, c web.Contents, on bool) (http.Handler, *http.Cookie, func(name string) db.File) {
	t.Helper()
	s, thumbs, meta := pieces(t)
	creds := credentials()
	ix := indexing(meta)
	ix.Contents, ix.Semantic = c, on
	h := web.Handler(web.Deps{
		Version: version, BuildDate: buildDate, Verifier: creds,
		Sessions: auth.NewSessions(creds, auth.DefaultSessionTTL), Shares: auth.NewShares(creds),
		Files: s, Thumbs: thumbs, Index: meta, Playlists: playlistsOf(meta), Indexing: ix,
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
