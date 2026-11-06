package web_test

import (
	"strings"
	"testing"
	"time"
)

// What this file is about: the square that has no picture in it. A grid of
// those used to be the browser's broken-image icon repeated, and what makes
// the mark show instead is two things a change to the markup can undo without
// failing anything else -- the glyph under the picture, and the empty alt that
// keeps a picture which never arrives from drawing over it (#324).

// The drawings themselves, as a piece of each path: which mark a page shows is
// the point, so a test that only counted them would pass with the wrong one.
const (
	photoMark = "M14 73l21-24"
	filmMark  = ">🎬</text>"
	coverMark = ">🎵</text>"
)

func TestAPhotoWithNoPictureShowsTheMarkInstead(t *testing.T) {
	t.Parallel()
	h, s, meta := browserOver(t)
	cookie := signIn(t, h)
	addPhoto(t, s, meta, "scan.tiff", time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), "")

	grid := get(t, h, "/photos/", cookie).Body.String()
	if strings.Contains(grid, "/thumb/scan.tiff") {
		t.Error("a thumbnail was offered for a file this build cannot decode")
	}
	if !strings.Contains(grid, photoMark) {
		t.Errorf("the tile is empty rather than a mark:\n%s", grid)
	}
	// And the name is still what the link is called, where a reader and a
	// pointer can both reach it.
	if !strings.Contains(grid, `class="visually-hidden">scan.tiff</span>`) {
		t.Error("the tile does not name the file to a screen reader")
	}
}

// TestAFilmWithNoPictureShowsTheFilmMark: the same square on the other
// library, and a different drawing -- which is the library's kind and not
// whether a duration has been read, since a recording nothing has looked at
// is still a film.
func TestAFilmWithNoPictureShowsTheFilmMark(t *testing.T) {
	t.Parallel()
	h, s, meta := browserOver(t)
	cookie := signIn(t, h)
	addVideo(t, s, meta, "holiday.avi", time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), 0)

	grid := get(t, h, "/videos/", cookie).Body.String()
	if !strings.Contains(grid, filmMark) || strings.Contains(grid, photoMark) {
		t.Errorf("a film with no picture is not drawn as one:\n%s", grid)
	}
}

// TestThePictureOverTheMarkIsDecorative is the half that is easy to undo by
// improving it: an <img> that fails with alt text draws the text and a
// broken-image icon over the mark, and one with alt="" draws nothing at all.
// So the name belongs in the link and the picture carries no alt, which is
// what makes a refused thumbnail look like a file with no picture rather than
// like a page that broke.
func TestThePictureOverTheMarkIsDecorative(t *testing.T) {
	t.Parallel()
	h, s, meta := browserOver(t)
	cookie := signIn(t, h)
	addPhoto(t, s, meta, "IMG_0001.jpg", time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), "")

	grid := get(t, h, "/photos/", cookie).Body.String()
	if !strings.Contains(grid, "/thumb/IMG_0001.jpg") {
		t.Fatalf("no thumbnail was offered at all:\n%s", grid)
	}
	if !strings.Contains(grid, `alt=""`) || strings.Contains(grid, `alt="IMG_0001.jpg"`) {
		t.Errorf("the picture is not decorative:\n%s", grid)
	}
	// The mark is under it either way, which is also what a grid shows while
	// it is still fetching.
	if !strings.Contains(grid, photoMark) {
		t.Error("the picture was drawn with nothing under it")
	}
}

// TestAnAlbumWithNoCoverShowsTheCoverMark: the same square again, on the pages
// where an empty one is the normal answer for half a library.
func TestAnAlbumWithNoCoverShowsTheCoverMark(t *testing.T) {
	t.Parallel()
	h, s, meta := browserOver(t)
	cookie := signIn(t, h)
	homework(t, s, meta)

	for _, at := range []string{"/music/AC_DC/", "/music/AC_DC/High%20Voltage/"} {
		if page := get(t, h, at, cookie).Body.String(); !strings.Contains(page, coverMark) {
			t.Errorf("%s draws an empty square rather than a mark:\n%s", at, page)
		}
	}
}

// TestAListingMarksWhatIsAPicture: the same mark in the row's little box, for
// the same reason -- the thumbnail may be refused or still on its way, and a
// row that said nothing would be an empty square either way. Every other file
// draws an emoji for its kind in the same box.
func TestAListingMarksWhatIsAPicture(t *testing.T) {
	t.Parallel()
	h, s, _ := browserOver(t)
	cookie := signIn(t, h)
	mkdir(t, s, "holiday")
	write(t, s, "holiday/notes.txt", "liner notes")
	write(t, s, "holiday/scan.tiff", "not really a photograph")
	write(t, s, "holiday/clip.avi", "not really a film")

	listing := get(t, h, "/files/holiday/", cookie).Body.String()
	for _, want := range []string{photoMark, filmMark} {
		if !strings.Contains(listing, want) {
			t.Errorf("the listing draws no mark for one of its two media files:\n%s", listing)
		}
	}
	if n := strings.Count(listing, photoMark) + strings.Count(listing, filmMark); n != 2 {
		t.Errorf("the listing drew %d marks, want one each for the photograph and the film", n)
	}
	if !strings.Contains(listing, `width="32" height="32" class="d-block" aria-hidden="true" focusable="false">
  <text x="16" y="16" text-anchor="middle" dominant-baseline="central" font-size="16">📄</text>`) {
		t.Errorf("the text file is not drawn as a document in the 32 box:\n%s", listing)
	}
}

// TestAListingMarksAFileByItsKind: the emoji a row without a picture gets.
func TestAListingMarksAFileByItsKind(t *testing.T) {
	t.Parallel()
	h, s, _ := browserOver(t)
	cookie := signIn(t, h)
	mkdir(t, s, "papers")
	mkdir(t, s, "papers/old")
	write(t, s, "papers/lease.pdf", "%PDF")
	write(t, s, "papers/backup.zip", "PK")

	listing := get(t, h, "/files/papers/", cookie).Body.String()
	for _, want := range []string{">📁</text>", ">📕</text>", ">📦</text>"} {
		if !strings.Contains(listing, want) {
			t.Errorf("the listing has no %s:\n%s", want, listing)
		}
	}
}

// TestATrackIsDrawnWithItsFolderCover: every track in the folder points at the
// cover's own row thumbnail, so the browser fetches one picture -- found even
// when the cover is on a page of the listing the tracks are not.
func TestATrackIsDrawnWithItsFolderCover(t *testing.T) {
	t.Parallel()
	h, s, _ := browserOver(t)
	cookie := signIn(t, h)
	mkdir(t, s, "album")
	for i := range 60 {
		write(t, s, "album/"+string(rune('a'+i/26))+string(rune('a'+i%26))+".mp3", "not really a track")
	}
	write(t, s, "album/cover.jpg", "pixels")

	listing := get(t, h, "/files/album/?rows=50", cookie).Body.String()
	if strings.Contains(listing, ">cover.jpg<") {
		t.Fatalf("the cover is on the first page, so this proves nothing:\n%s", listing)
	}
	if n := strings.Count(listing, `<img src="/thumb/album/cover.jpg?size=96`); n != 50 {
		t.Errorf("%d tracks were drawn with the folder's cover, want all 50", n)
	}
	if n := strings.Count(listing, coverMark); n != 50 {
		t.Errorf("%d tracks have the note under their cover, want all 50", n)
	}
}

// TestATrackWithNoCoverIsTheNote: no picture in the folder is the mark alone,
// and no per-track request for one.
func TestATrackWithNoCoverIsTheNote(t *testing.T) {
	t.Parallel()
	h, s, _ := browserOver(t)
	cookie := signIn(t, h)
	mkdir(t, s, "album")
	write(t, s, "album/01.mp3", "not really a track")

	listing := get(t, h, "/files/album/", cookie).Body.String()
	if !strings.Contains(listing, coverMark) {
		t.Errorf("the track is not drawn as one:\n%s", listing)
	}
	if strings.Contains(listing, "/thumb/") {
		t.Errorf("a picture was offered for a track with no cover:\n%s", listing)
	}
}

// TestAListingPictureIsOverItsMark is the row's half of what keeps a refused
// thumbnail from drawing a broken icon: the picture is positioned over the
// mark and carries no alt text.
func TestAListingPictureIsOverItsMark(t *testing.T) {
	t.Parallel()
	h, s, _ := browserOver(t)
	cookie := signIn(t, h)
	mkdir(t, s, "holiday")
	write(t, s, "holiday/IMG_0001.jpg", "pixels")

	listing := get(t, h, "/files/holiday/", cookie).Body.String()
	if !strings.Contains(listing, photoMark) {
		t.Errorf("the row has a thumbnail and no mark under it:\n%s", listing)
	}
	if !strings.Contains(listing, `position-absolute top-0 start-0`) {
		t.Error("the thumbnail is not drawn over the mark")
	}
	if strings.Contains(listing, `alt="IMG_0001.jpg"`) {
		t.Error("the thumbnail is not decorative, so a refused one would draw over the mark")
	}
}
