package faces_test

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/dbtest"
	"github.com/stratus-private-cloud/stratus-backend/internal/db/sqlite"
	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/faces"
	"github.com/stratus-private-cloud/stratus-backend/internal/media"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
	"github.com/stratus-private-cloud/stratus-backend/internal/storage"
	"github.com/stratus-private-cloud/stratus-backend/internal/storage/disk"
)

// The fake models see colour: a picture with a face in it is red or blue all
// over, and the face's vector points at its colour. Green has nobody in it.
type models struct {
	fail error
}

func (m *models) Detect(_ context.Context, bgr []float32) ([][]float32, error) {
	if m.fail != nil {
		return nil, m.fail
	}
	outs := make([][]float32, 12)
	for k, s := range []int{8, 16, 32} {
		n := (embed.DetectSize / s) * (embed.DetectSize / s)
		outs[k], outs[3+k], outs[6+k], outs[9+k] = make([]float32, n), make([]float32, n), make([]float32, 4*n), make([]float32, 10*n)
	}
	plane := embed.DetectSize * embed.DetectSize
	if bgr[plane] > 100 { // green
		return outs, nil
	}
	// One face of 64 at stride 16, around (328, 168) of the square.
	i := 10*40 + 20
	outs[1][i], outs[4][i] = 0.99, 0.99
	copy(outs[7][4*i:], []float32{0.5, 0.5, float32(math.Log(4)), float32(math.Log(4))})
	for n, p := range [5][2]float32{{-0.6, -0.4}, {0.6, -0.4}, {0, 0}, {-0.5, 0.6}, {0.5, 0.6}} {
		outs[10][10*i+2*n], outs[10][10*i+2*n+1] = 0.5+p[0], 0.5+p[1]
	}
	return outs, nil
}

func (m *models) Recognize(_ context.Context, rgb []float32) ([]float32, error) {
	if m.fail != nil {
		return nil, m.fail
	}
	v := make([]float32, faces.Dimensions)
	plane := embed.FaceSize * embed.FaceSize
	centre := 56*embed.FaceSize + 56
	if rgb[centre] > rgb[2*plane+centre] {
		v[0] = 3
	} else {
		v[1] = 3
	}
	return v, nil
}

type pictures struct {
	colours map[int64]color.Color
	busy    bool
	refuse  map[int64]bool
	lost    map[int64]bool
}

func (p *pictures) Look(_ context.Context, f db.File, size int, fn func(image.Image) error) error {
	if p.busy {
		return media.ErrBusy
	}
	if p.refuse[f.ID] {
		return thumb.ErrNoThumbnail
	}
	if p.lost[f.ID] {
		return errors.New("the store did not answer")
	}
	if size != faces.PictureSize {
		return errors.New("asked for the wrong size")
	}
	return fn(picture(p.colours[f.ID]))
}

func picture(c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 640, 640))
	for y := range 640 {
		for x := range 640 {
			img.Set(x, y, c)
		}
	}
	return img
}

var (
	red   = color.RGBA{R: 220, A: 255}
	blue  = color.RGBA{B: 220, A: 255}
	green = color.RGBA{G: 220, A: 255}
)

type fixture struct {
	store    *sqlite.Store
	blobs    *disk.Store
	pictures *pictures
	models   *models
	service  *faces.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store, err := sqlite.New(t.Context(), filepath.Join(t.TempDir(), "stratus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	blobs, err := disk.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &pictures{colours: map[int64]color.Color{}, refuse: map[int64]bool{}, lost: map[int64]bool{}}
	m := &models{}
	return &fixture{store: store, blobs: blobs, pictures: p, models: m, service: faces.New(store, blobs, p, m, "fake-1")}
}

func (fx *fixture) photo(t *testing.T, name string, c color.Color) db.File {
	t.Helper()
	f, err := fx.store.PutFile(t.Context(), db.File{
		OwnerID: "edu", Path: name, BlobKey: "blobs/" + name, Size: 10,
		MTime: time.Now(), ETag: `"` + name + `"`, MIMEType: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.store.PutMedia(t.Context(), db.Media{
		FileID: f.ID, Kind: db.KindImage, IndexedAt: time.Now(), Version: 1, ETag: f.ETag,
	}); err != nil {
		t.Fatal(err)
	}
	fx.pictures.colours[f.ID] = c
	return f
}

func (fx *fixture) read(t *testing.T, f db.File) {
	t.Helper()
	if err := fx.service.Read(t.Context(), f, picture(fx.pictures.colours[f.ID])); err != nil {
		t.Fatalf("Read(%s): %v", f.Path, err)
	}
}

func (fx *fixture) hasCrop(t *testing.T, face db.Face) bool {
	t.Helper()
	_, err := fx.blobs.Stat(t.Context(), faces.CropKey(face.BlobKey, face.ID))
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func TestAPhotographsFacesAreWrittenWithTheirPictures(t *testing.T) {
	fx := newFixture(t)
	f := fx.photo(t, "ana.jpg", red)
	if ok, err := fx.service.Wants(t.Context(), f); err != nil || !ok {
		t.Fatalf("Wants = %v, %v before any reading", ok, err)
	}
	fx.read(t, f)

	found, err := fx.service.FacesOf(t.Context(), f)
	if err != nil || len(found) != 1 {
		t.Fatalf("FacesOf = %v, %v; want one face", found, err)
	}
	first := found[0]
	if math.Abs(float64(first.X)-0.4625) > 0.001 || math.Abs(float64(first.W)-0.1) > 0.001 {
		t.Errorf("the box is %+v, want x 0.4625 and width 0.1 of the picture", first)
	}
	body, size, err := fx.service.Crop(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 2)
	_, _ = io.ReadFull(body, head)
	_ = body.Close()
	if size == 0 || head[0] != 0xff || head[1] != 0xd8 {
		t.Errorf("the picture of the face is %d bytes starting %x, want a JPEG", size, head)
	}
	if ok, werr := fx.service.Wants(t.Context(), f); werr != nil || ok {
		t.Errorf("Wants = %v, %v after reading", ok, werr)
	}

	// Read again, it is replaced, and so is its picture.
	fx.read(t, f)
	again, err := fx.service.FacesOf(t.Context(), f)
	if err != nil || len(again) != 1 || again[0].ID == first.ID {
		t.Fatalf("FacesOf after a second reading = %v, %v", again, err)
	}
	if fx.hasCrop(t, first) || !fx.hasCrop(t, again[0]) {
		t.Errorf("the old picture is kept or the new one is missing")
	}

	nobody := fx.photo(t, "field.jpg", green)
	fx.read(t, nobody)
	if c, err := fx.service.Counts(t.Context()); err != nil || c.Scanned != 2 || c.Faces != 1 {
		t.Errorf("Counts = %+v, %v; want two read and one face", c, err)
	}
}

func TestAHelperThatWillNotRunWritesNothing(t *testing.T) {
	fx := newFixture(t)
	f := fx.photo(t, "ana.jpg", red)
	fx.models.fail = embed.ErrUnavailable
	if err := fx.service.Read(t.Context(), f, picture(red)); !errors.Is(err, embed.ErrUnavailable) {
		t.Fatalf("Read = %v, want ErrUnavailable", err)
	}
	if ok, _ := fx.service.Wants(t.Context(), f); !ok {
		t.Error("the photograph is not wanted any more after a helper that did not run")
	}

	fx.models.fail = errors.New("not a picture this model takes")
	if err := fx.service.Read(t.Context(), f, picture(red)); err != nil {
		t.Fatal(err)
	}
	if c, _ := fx.service.Counts(t.Context()); c.Refused != 1 {
		t.Errorf("Counts = %+v, want the refusal", c)
	}
}

func TestAPassLooksAtWhatIsPending(t *testing.T) {
	fx := newFixture(t)
	fx.photo(t, "a.jpg", red)
	unreadable := fx.photo(t, "b.heic", red)
	fx.pictures.refuse[unreadable.ID] = true

	fx.pictures.busy = true
	if n, err := fx.service.Batch(t.Context(), time.Now()); !errors.Is(err, media.ErrBusy) || n != 0 {
		t.Errorf("Batch while busy = %d, %v", n, err)
	}
	fx.pictures.busy = false
	if n, err := fx.service.Batch(t.Context(), time.Now()); err != nil || n != 2 {
		t.Fatalf("Batch = %d, %v; want both", n, err)
	}
	if c, _ := fx.service.Counts(t.Context()); c.Scanned != 1 || c.Refused != 1 || c.Waiting() != 0 {
		t.Errorf("Counts = %+v", c)
	}
	if n, err := fx.service.Batch(t.Context(), time.Now()); err != nil || n != 0 {
		t.Errorf("a second Batch = %d, %v; want nothing left", n, err)
	}
}

func TestFacesAreGroupedNamedMergedAndForgotten(t *testing.T) {
	fx := newFixture(t)
	for _, name := range []string{"a1.jpg", "a2.jpg", "a3.jpg", "b1.jpg", "b2.jpg", "b3.jpg"} {
		c := red
		if name[0] == 'b' {
			c = blue
		}
		fx.read(t, fx.photo(t, name, c))
	}
	if !fx.service.Grouping(true) || fx.service.Grouping(false) {
		t.Error("six faces found: a round is due once the pass is done, and not before")
	}
	if err := fx.service.Group(t.Context()); err != nil {
		t.Fatal(err)
	}
	people, err := fx.service.People(t.Context())
	if err != nil || len(people) != 2 || people[0].Faces != 3 {
		t.Fatalf("People = %+v, %v; want two of three faces", people, err)
	}
	if fx.service.Grouping(true) {
		t.Error("a round is due with nothing new")
	}

	// A fourth of them joins, and a name holds.
	late := fx.photo(t, "a4.jpg", red)
	fx.read(t, late)
	if err = fx.service.Rename(t.Context(), people[0].ID, "  Ana  "); err != nil {
		t.Fatal(err)
	}
	if err = fx.service.Rename(t.Context(), people[0].ID, "a/b"); err == nil {
		t.Error("a name with a slash was taken")
	}
	if err = fx.service.Group(t.Context()); err != nil {
		t.Fatal(err)
	}
	people, _ = fx.service.People(t.Context())
	if people[0].Name != "Ana" || people[0].Faces != 4 {
		t.Errorf("People = %+v, want Ana with four", people)
	}

	// A face of Ana's taken out of her is loose, a round is due, and the
	// round does not put it back.
	page, err := fx.service.PersonFaces(t.Context(), people[0].ID, db.FaceCursor{}, 10)
	if err != nil || len(page) != 4 {
		t.Fatalf("PersonFaces = %d, %v", len(page), err)
	}
	if err = fx.service.Remove(t.Context(), people[0].ID, []int64{page[0].ID}); err != nil {
		t.Fatal(err)
	}
	if !fx.service.Grouping(true) {
		t.Error("a face taken out of somebody makes no round due")
	}
	if err = fx.service.Group(t.Context()); err != nil {
		t.Fatal(err)
	}
	people, _ = fx.service.People(t.Context())
	if people[0].Faces != 3 {
		t.Errorf("People after taking a face out = %+v, want Ana with three", people)
	}

	if err = fx.service.Merge(t.Context(), people[1].ID, people[1].ID); err != nil {
		t.Errorf("merging somebody into themselves = %v", err)
	}
	if err = fx.service.Merge(t.Context(), people[1].ID, people[0].ID); err != nil {
		t.Fatal(err)
	}
	people, _ = fx.service.People(t.Context())
	if len(people) != 1 || people[0].Faces != 6 {
		t.Errorf("People after a merge = %+v, want one of six", people)
	}
	page, err = fx.service.PersonFaces(t.Context(), people[0].ID, db.FaceCursor{}, 10)
	if err != nil || len(page) != 6 {
		t.Errorf("PersonFaces = %d, %v", len(page), err)
	}

	if err := faces.Forget(t.Context(), fx.store, fx.blobs); err != nil {
		t.Fatal(err)
	}
	if left, _ := fx.service.FacesOf(t.Context(), late); len(left) != 0 {
		t.Errorf("faces after Forget: %v", left)
	}
	if fx.hasCrop(t, page[0]) {
		t.Error("a picture of a face is still in the store after Forget")
	}
	if c, _ := fx.service.Counts(t.Context()); c.Scanned != 0 || c.People != 0 {
		t.Errorf("Counts after Forget = %+v", c)
	}
}

// TestAStoreThatFailsLeavesNothingHalfDone: each call the pass, the grouping
// and the forgetting make, broken in turn, comes back as an error -- and a
// photograph whose faces could not be written is still wanted.
func TestAStoreThatFailsLeavesNothingHalfDone(t *testing.T) {
	for _, method := range []string{"PendingFaceScans", "FacesOf", "AddFace"} {
		t.Run(method, func(t *testing.T) {
			fx := newFixture(t)
			f := fx.photo(t, "ana.jpg", red)
			s := faces.New(dbtest.FailOn(t, fx.store, method), fx.blobs, fx.pictures, fx.models, "fake-1")
			if _, err := s.Batch(t.Context(), time.Now()); method == "PendingFaceScans" && err == nil {
				t.Error("Batch over a queue it could not read reported nothing")
			}
			if ok, _ := fx.service.Wants(t.Context(), f); !ok {
				t.Errorf("with %s broken the photograph was written down anyway", method)
			}
		})
	}

	for _, method := range []string{"EachFace", "NewPerson", "DeleteEmptyPeople"} {
		t.Run(method, func(t *testing.T) {
			fx := newFixture(t)
			for _, name := range []string{"a1.jpg", "a2.jpg", "a3.jpg"} {
				fx.read(t, fx.photo(t, name, red))
			}
			s := faces.New(dbtest.FailOn(t, fx.store, method), fx.blobs, fx.pictures, fx.models, "fake-1")
			if err := s.Group(t.Context()); err == nil {
				t.Errorf("Group with %s broken reported nothing", method)
			}
		})
	}

	for _, method := range []string{"EachFaceCrop", "ForgetFaces"} {
		t.Run(method, func(t *testing.T) {
			fx := newFixture(t)
			fx.read(t, fx.photo(t, "ana.jpg", red))
			if err := faces.Forget(t.Context(), dbtest.FailOn(t, fx.store, method), fx.blobs); err == nil {
				t.Errorf("Forget with %s broken reported nothing", method)
			}
		})
	}

	fx := newFixture(t)
	s := faces.New(dbtest.FailOn(t, fx.store, "People"), fx.blobs, fx.pictures, fx.models, "fake-1")
	if _, err := s.People(t.Context()); err == nil {
		t.Error("People over a store that failed reported nothing")
	}
	s = faces.New(dbtest.FailOn(t, fx.store, "FacesOf"), fx.blobs, fx.pictures, fx.models, "fake-1")
	if _, err := s.FacesOf(t.Context(), db.File{ID: 1}); err == nil {
		t.Error("FacesOf over a store that failed reported nothing")
	}
	s = faces.New(dbtest.FailOn(t, fx.store, "Face"), fx.blobs, fx.pictures, fx.models, "fake-1")
	if _, _, err := s.Crop(t.Context(), 1); err == nil {
		t.Error("Crop over a store that failed reported nothing")
	}
	if _, _, err := fx.service.Crop(t.Context(), 1<<40); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("the picture of no face = %v, want ErrNotFound", err)
	}
	if fx.service.Size() != faces.PictureSize || fx.service.Model() != "fake-1"+faces.Pipeline || fx.service.State() != "" {
		t.Errorf("Size %d, Model %q, State %q", fx.service.Size(), fx.service.Model(), fx.service.State())
	}
}

// counting is a store that says how many times everybody was listed.
type counting struct {
	faces.Store
	listings int
}

func (c *counting) People(ctx context.Context) ([]db.Person, error) {
	c.listings++
	return c.Store.People(ctx)
}

// TestAPageOfPicturesListsEverybodyOnce: each picture on a page of people is
// asked for by its person's name, so the listing that resolves it is kept a
// moment -- and thrown away by anything that changes who is who.
func TestAPageOfPicturesListsEverybodyOnce(t *testing.T) {
	fx := newFixture(t)
	for _, name := range []string{"a1.jpg", "a2.jpg", "a3.jpg"} {
		fx.read(t, fx.photo(t, name, red))
	}
	store := &counting{Store: fx.store}
	s := faces.New(store, fx.blobs, fx.pictures, fx.models, "fake-1")
	if err := s.Group(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if people, err := s.People(t.Context()); err != nil || len(people) != 1 {
			t.Fatalf("People = %+v, %v", people, err)
		}
	}
	if store.listings != 1 {
		t.Errorf("twenty pictures listed everybody %d times, want once", store.listings)
	}

	people, _ := s.People(t.Context())
	people[0].Name = "changed by a caller"
	if again, _ := s.People(t.Context()); again[0].Name != "" {
		t.Error("a caller's change reached the kept listing")
	}

	if err := s.Rename(t.Context(), people[0].ID, "Ana"); err != nil {
		t.Fatal(err)
	}
	if renamed, _ := s.People(t.Context()); renamed[0].Name != "Ana" || store.listings != 2 {
		t.Errorf("after a rename People = %+v in %d listings, want Ana in two", renamed, store.listings)
	}
}

// TestABatchIsNotHeldByWhatItCannotRead is #557 here too: what could not be
// read is put off, and what is older is looked at.
func TestABatchIsNotHeldByWhatItCannotRead(t *testing.T) {
	t.Parallel()
	fx := newFixture(t)
	fx.photo(t, "older.jpg", red)
	for i := range faces.Batch {
		f := fx.photo(t, fmt.Sprintf("lost-%d.jpg", i), red)
		fx.pictures.lost[f.ID] = true
	}
	now := time.Now()
	if n, err := fx.service.Batch(t.Context(), now); err != nil || n != 0 {
		t.Fatalf("a batch of nothing readable = %d, %v", n, err)
	}
	if n, err := fx.service.Batch(t.Context(), now.Add(time.Second)); err != nil || n != 1 {
		t.Errorf("the next batch = %d, %v; want the older photograph looked at", n, err)
	}
}
