package dav_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/dav"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/dbtest"
)

const askForInfo = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:" xmlns:S="https://github.com/C0piIot/stratus"><D:prop>
<D:getetag/><S:taken/><S:width/><S:height/><S:camera/><S:location/><S:place/><S:duration/>
</D:prop></D:propfind>`

var holiday = db.Media{
	Kind: db.KindImage, IndexedAt: time.Now(), Version: 1,
	TakenAt: time.Date(2024, 6, 15, 18, 30, 5, 0, time.UTC),
	Width:   4032, Height: 3024, Camera: "Apple iPhone 15 & Co",
	GPS: &db.GPS{Latitude: 41.385063, Longitude: 2.173404},
}

var holidayInfo = map[string]string{
	"taken": "2024-06-15T18:30:05", "width": "4032", "height": "3024",
	"camera": "Apple iPhone 15 & Co", "location": "41.38506 2.17340", "place": "Barcelona, Spain",
}

func wantInfo(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	for _, name := range []string{"taken", "width", "height", "camera", "location", "place", "duration"} {
		if got[name] != want[name] {
			t.Errorf("%s = %q, want %q", name, got[name], want[name])
		}
	}
}

// TestAFileSaysWhatTheIndexerRead is the web's Details over WebDAV: asked for
// by name, a photograph answers when, how big, which camera and where, and a
// fact it does not carry is absent rather than empty.
func TestAFileSaysWhatTheIndexerRead(t *testing.T) {
	t.Parallel()
	svc, store := service(t)
	h := withUser(dav.Handler(prefix, svc, store), "edu")
	do(t, h, http.MethodPut, "/files/photo.jpg", "not really a photograph")
	do(t, h, http.MethodPut, "/files/notes.txt", "nothing to read")
	f, err := store.FileByPath(t.Context(), "edu", "photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	m := holiday
	m.FileID, m.ETag = f.ID, f.ETag
	if err = store.PutMedia(t.Context(), m); err != nil {
		t.Fatal(err)
	}

	got := propsOf(t, do(t, h, "PROPFIND", "/files/photo.jpg", askForInfo, "Depth", "0",
		"Content-Type", "application/xml").Body.String())["/files/photo.jpg"]
	wantInfo(t, got, holidayInfo)
	if got["getetag"] == "" {
		t.Error("the properties of the protocol went missing beside ours")
	}

	// A recording says how long it is, and nothing it does not know.
	do(t, h, http.MethodPut, "/files/clip.mp4", "not really a recording")
	clip, err := store.FileByPath(t.Context(), "edu", "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	video := db.Media{FileID: clip.ID, ETag: clip.ETag, Kind: db.KindVideo, IndexedAt: time.Now(), Version: 1, DurationMS: 12500}
	if err = store.PutMedia(t.Context(), video); err != nil {
		t.Fatal(err)
	}
	wantInfo(t, propsOf(t, do(t, h, "PROPFIND", "/files/clip.mp4", askForInfo, "Depth", "0",
		"Content-Type", "application/xml").Body.String())["/files/clip.mp4"], map[string]string{"duration": "12500"})

	// A file the indexer has nothing on answers none of them, and still answers.
	rec := do(t, h, "PROPFIND", "/files/notes.txt", askForInfo, "Depth", "0", "Content-Type", "application/xml")
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND of an unread file = %d", rec.Code)
	}
	wantInfo(t, propsOf(t, rec.Body.String())["/files/notes.txt"], nil)
}

// TestTheInfoIsOnlyForWhoeverAsks: an allprop is every listing Finder makes,
// and a query per row to hand it facts it throws away is the cost these
// properties are kept out of it to avoid.
func TestTheInfoIsOnlyForWhoeverAsks(t *testing.T) {
	t.Parallel()
	svc, store := service(t)
	broken := dbtest.FailOn(t, store, "MediaByFile")
	h := withUser(dav.Handler(prefix, svc, broken), "edu")
	do(t, h, "MKCOL", "/files/album", "")
	do(t, h, http.MethodPut, "/files/album/photo.jpg", "not really a photograph")

	rec := do(t, h, "PROPFIND", "/files/album", "", "Depth", "1", "Content-Type", "application/xml")
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("an allprop asked the index for the info: %d", rec.Code)
	}
	if _, there := propsOf(t, rec.Body.String())["/files/album/photo.jpg"]["taken"]; there {
		t.Error("an allprop carries the info")
	}
}

// TestADatedPhotographSaysItToo: the timeline already read the metadata to
// file the photograph under its month, so the same properties come with it.
func TestADatedPhotographSaysItToo(t *testing.T) {
	t.Parallel()
	r := byDateServer(t)
	f := r.add(t, "Camera/IMG_0001.JPG", "one", "image/jpeg", holiday.TakenAt)
	m := holiday
	m.FileID, m.ETag = f.ID, f.ETag
	if err := r.meta.PutMedia(t.Context(), m); err != nil {
		t.Fatal(err)
	}

	listed := propsOf(t, do(t, r.h, "PROPFIND", "/photos/2024/06/", askForInfo, "Depth", "1",
		"Content-Type", "application/xml").Body.String())
	wantInfo(t, listed["/photos/2024/06/IMG_0001.JPG"], holidayInfo)

	plain := propsOf(t, do(t, r.h, "PROPFIND", "/photos/2024/06/", "", "Depth", "1").Body.String())
	if _, there := plain["/photos/2024/06/IMG_0001.JPG"]["taken"]; there {
		t.Error("an allprop of the timeline carries the info")
	}
}
