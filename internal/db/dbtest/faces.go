package dbtest

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// RunFaces executes the face cases against the repository built by newRepo
// (#519).
func RunFaces(t *testing.T, newRepo func(t *testing.T) db.Repo) {
	t.Helper()

	cases := []struct {
		name string
		fn   func(t *testing.T, s db.Repo)
	}{
		{"a scan takes a photograph out of the queue by the embeddings' rules", facesQueue},
		{"a face comes back as it went in, and goes with its file", facesRoundTrip},
		{"a face of bytes an overwrite replaced is not current", facesStale},
		{"grouping fills only faces nobody has", facesAssign},
		{"people are named first and counted by their current faces", facesPeople},
		{"a person's faces are paged newest photograph first", facesPersonPages},
		{"merging moves the faces and forgets the person", facesMerge},
		{"a face taken out of somebody is kept out of them", facesRemove},
		{"merging keeps a face out of whoever it was kept out of", facesMergeExclusions},
		{"an exclusion goes with its face and its person", facesExclusionsCascade},
		{"forgetting deletes every face, scan and person", facesForget},
		{"the counts are the photographs, the scans and what they found", facesCounted},
		{"a walk stops at its callback's error", facesFnErrors},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.fn(t, newRepo(t))
		})
	}
}

const faceModel = "faces-test"

func scanned(t *testing.T, s db.Repo, f db.File, state db.EmbeddingState, at time.Time) {
	t.Helper()
	err := s.PutFaceScan(t.Context(), db.FaceScan{FileID: f.ID, State: state, Model: faceModel, BlobKey: f.BlobKey, At: at})
	if err != nil {
		t.Fatalf("PutFaceScan: %v", err)
	}
}

func addFace(t *testing.T, s db.Repo, f db.File, score float32, person int64) db.Face {
	t.Helper()
	face := db.Face{
		FileID: f.ID, BlobKey: f.BlobKey, X: 0.25, Y: 0.125, W: 0.5, H: 0.375, Score: score,
		Vector: vectorOf(byte(f.ID))[:132], Person: person, //nolint:gosec // a pattern, not a number
	}
	id, err := s.AddFace(t.Context(), face)
	if err != nil {
		t.Fatalf("AddFace: %v", err)
	}
	face.ID = id
	return face
}

func newPerson(t *testing.T, s db.Repo, name string) int64 {
	t.Helper()
	id, err := s.NewPerson(t.Context(), name)
	if err != nil {
		t.Fatalf("NewPerson: %v", err)
	}
	return id
}

func faceIDs(faces []db.Face) []int64 {
	ids := make([]int64, len(faces))
	for i, f := range faces {
		ids[i] = f.ID
	}
	return ids
}

func eachFace(t *testing.T, s db.Repo, model string) []db.Face {
	t.Helper()
	var out []db.Face
	if err := s.EachFace(t.Context(), model, func(f db.Face) error { out = append(out, f); return nil }); err != nil {
		t.Fatalf("EachFace: %v", err)
	}
	return out
}

func facesQueue(t *testing.T, s db.Repo) {
	made := photoAt(t, s, "made.jpg", time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	refused := photoAt(t, s, "refused.jpg", time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
	fresh := photoAt(t, s, "fresh.jpg", time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC))
	at := time.Now()
	scanned(t, s, made, db.EmbeddingMade, at)
	scanned(t, s, refused, db.EmbeddingRefused, at)

	pending := func(model string, staleBefore time.Time) []int64 {
		files, err := s.PendingFaceScans(t.Context(), model, staleBefore, 10)
		if err != nil {
			t.Fatalf("PendingFaceScans: %v", err)
		}
		ids := make([]int64, len(files))
		for i, f := range files {
			ids[i] = f.ID
		}
		return ids
	}
	if got := pending(faceModel, at.Add(-time.Hour)); !equalIDs(got, []int64{fresh.ID}) {
		t.Errorf("PendingFaceScans = %v, want [%d]", got, fresh.ID)
	}
	if got := pending(faceModel, at.Add(time.Hour)); !equalIDs(got, []int64{refused.ID, fresh.ID}) {
		t.Errorf("PendingFaceScans once the refusal is stale = %v, want [%d %d]", got, refused.ID, fresh.ID)
	}
	if got := pending("another-model", at); !equalIDs(got, []int64{made.ID, refused.ID, fresh.ID}) {
		t.Errorf("PendingFaceScans for another model = %v, want all three, newest first", got)
	}
	for f, want := range map[db.File]bool{made: false, refused: false, fresh: true} {
		ok, err := s.WantsFaceScan(t.Context(), f.ID, faceModel, at.Add(-time.Hour))
		if err != nil || ok != want {
			t.Errorf("WantsFaceScan(%s) = %v, %v; want %v", f.Path, ok, err, want)
		}
	}

	if err := s.PutFaceScan(t.Context(), db.FaceScan{FileID: 1 << 40, State: db.EmbeddingMade, Model: faceModel,
		BlobKey: "x", At: at}); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("PutFaceScan of a file with no media row = %v, want ErrNotFound", err)
	}
}

func equalIDs(a, b []int64) bool { return slices.Equal(a, b) }

func facesRoundTrip(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC))
	scanned(t, s, photo, db.EmbeddingMade, time.Now())
	want := addFace(t, s, photo, 0.93, 0)
	addFace(t, s, photo, 0.91, 0)

	got, err := s.Face(t.Context(), want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FileID != want.FileID || got.BlobKey != want.BlobKey || got.X != want.X || got.Y != want.Y ||
		got.W != want.W || got.H != want.H || got.Score != want.Score || got.Vector != nil ||
		got.Person != 0 || !got.SortAt.Equal(time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Face = %+v, want %+v", got, want)
	}
	if of, err := s.FacesOf(t.Context(), photo.ID); err != nil || len(of) != 2 || of[0].ID != want.ID {
		t.Errorf("FacesOf = %v, %v; want both, oldest first", faceIDs(of), err)
	}
	if each := eachFace(t, s, faceModel); len(each) != 2 || !bytes.Equal(each[0].Vector, want.Vector) {
		t.Errorf("EachFace = %v, want both, with their vectors", faceIDs(each))
	}
	if each := eachFace(t, s, "another-model"); len(each) != 0 {
		t.Errorf("EachFace for another model = %v, want none", faceIDs(each))
	}

	if err := s.DeleteFacesOf(t.Context(), photo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Face(t.Context(), want.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Face after DeleteFacesOf = %v, want ErrNotFound", err)
	}

	addFace(t, s, photo, 0.9, 0)
	if err := s.DeleteFile(t.Context(), owner, photo.Path); err != nil {
		t.Fatal(err)
	}
	if of, err := s.FacesOf(t.Context(), photo.ID); err != nil || len(of) != 0 {
		t.Errorf("FacesOf a deleted file = %v, %v; want none", faceIDs(of), err)
	}
}

func facesStale(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	scanned(t, s, photo, db.EmbeddingMade, time.Now())
	person := newPerson(t, s, "Ana")
	face := addFace(t, s, photo, 0.9, person)

	replacement := file("photo.jpg")
	replacement.ETag = `"another photograph"`
	replacement.BlobKey = "blobs/photo-2"
	put(t, s, replacement)

	if each := eachFace(t, s, faceModel); len(each) != 0 {
		t.Errorf("EachFace after an overwrite = %v, want none", faceIDs(each))
	}
	if p, err := s.Person(t.Context(), person); err != nil || p.Faces != 0 || p.Cover != 0 {
		t.Errorf("Person after an overwrite = %+v, %v; want no faces", p, err)
	}
	// Still there, for whoever deletes its picture.
	var crops []int64
	if err := s.EachFaceCrop(t.Context(), func(id int64, key string) error {
		if key != photo.BlobKey {
			t.Errorf("crop %d under %q, want the old bytes' %q", id, key, photo.BlobKey)
		}
		crops = append(crops, id)
		return nil
	}); err != nil || !equalIDs(crops, []int64{face.ID}) {
		t.Errorf("EachFaceCrop = %v, %v; want [%d]", crops, err, face.ID)
	}
}

func facesAssign(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	ana, bea := newPerson(t, s, ""), newPerson(t, s, "")
	loose := addFace(t, s, photo, 0.9, 0)
	taken := addFace(t, s, photo, 0.9, bea)

	if err := s.AssignFaces(t.Context(), ana, []int64{loose.ID, taken.ID}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]int64{loose.ID: ana, taken.ID: bea} {
		if f, err := s.Face(t.Context(), id); err != nil || f.Person != want {
			t.Errorf("face %d belongs to %d (%v), want %d", id, f.Person, err, want)
		}
	}
	if err := s.AssignFaces(t.Context(), ana, nil); err != nil {
		t.Errorf("AssignFaces of nothing = %v", err)
	}
}

func facesPeople(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	many, few, named := newPerson(t, s, ""), newPerson(t, s, ""), newPerson(t, s, "zoe")
	newPerson(t, s, "")
	addFace(t, s, photo, 0.8, many)
	best := addFace(t, s, photo, 0.99, many)
	addFace(t, s, photo, 0.9, many)
	addFace(t, s, photo, 0.9, few)
	addFace(t, s, photo, 0.9, named)

	if err := s.RenamePerson(t.Context(), named, "Zoe"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenamePerson(t.Context(), 1<<40, "Nobody"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("RenamePerson of nobody = %v, want ErrNotFound", err)
	}
	if n, err := s.DeleteEmptyPeople(t.Context()); err != nil || n != 1 {
		t.Errorf("DeleteEmptyPeople = %d, %v; want the one with no faces", n, err)
	}

	people, err := s.People(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(people))
	for _, p := range people {
		ids = append(ids, p.ID)
	}
	if !equalIDs(ids, []int64{named, many, few}) {
		t.Fatalf("People = %v, want [%d %d %d]: the named, then the most faces", ids, named, many, few)
	}
	if people[0].Name != "Zoe" || people[1].Faces != 3 || people[1].Cover != best.ID {
		t.Errorf("People = %+v", people)
	}
	if _, err := s.Person(t.Context(), 1<<40); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Person of nobody = %v, want ErrNotFound", err)
	}
}

func facesPersonPages(t *testing.T, s db.Repo) {
	person := newPerson(t, s, "")
	var want []int64
	for i, year := range []int{2024, 2022, 2023} {
		photo := photoAt(t, s, string(rune('a'+i))+".jpg", time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC))
		addFace(t, s, photo, 0.9, person)
	}
	all, err := s.PersonFaces(t.Context(), person, db.FaceCursor{}, 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("PersonFaces = %v, %v", faceIDs(all), err)
	}
	for i := 1; i < len(all); i++ {
		if !all[i].SortAt.Before(all[i-1].SortAt) {
			t.Errorf("PersonFaces is not newest first: %v", all)
		}
	}
	want = faceIDs(all)

	first, err := s.PersonFaces(t.Context(), person, db.FaceCursor{}, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page = %v, %v", faceIDs(first), err)
	}
	last := first[len(first)-1]
	rest, err := s.PersonFaces(t.Context(), person, db.FaceCursor{SortAt: last.SortAt, ID: last.ID}, 2)
	if err != nil || !equalIDs(append(faceIDs(first), faceIDs(rest)...), want) {
		t.Errorf("two pages = %v + %v, %v; want %v", faceIDs(first), faceIDs(rest), err, want)
	}
}

func facesMerge(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	from, into := newPerson(t, s, "Ana"), newPerson(t, s, "Ana María")
	moved := addFace(t, s, photo, 0.9, from)

	if err := s.MergePeople(t.Context(), from, 1<<40); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("MergePeople into nobody = %v, want ErrNotFound", err)
	}
	if f, err := s.Face(t.Context(), moved.ID); err != nil || f.Person != from {
		t.Errorf("a refused merge moved face %d to %d (%v)", moved.ID, f.Person, err)
	}
	if err := s.MergePeople(t.Context(), from, into); err != nil {
		t.Fatal(err)
	}
	if f, err := s.Face(t.Context(), moved.ID); err != nil || f.Person != into {
		t.Errorf("face %d belongs to %d (%v), want %d", moved.ID, f.Person, err, into)
	}
	if _, err := s.Person(t.Context(), from); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Person after merging them away = %v, want ErrNotFound", err)
	}
}

// exclusions is every face and who it is kept out of, as "face:person".
func exclusions(t *testing.T, s db.Repo) []string {
	t.Helper()
	var out []string
	err := s.EachFaceExclusion(t.Context(), func(face, person int64) error {
		out = append(out, fmt.Sprintf("%d:%d", face, person))
		return nil
	})
	if err != nil {
		t.Fatalf("EachFaceExclusion: %v", err)
	}
	return out
}

func facesRemove(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	ana, bea := newPerson(t, s, "Ana"), newPerson(t, s, "Bea")
	stranger := addFace(t, s, photo, 0.9, ana)
	kept := addFace(t, s, photo, 0.9, ana)
	beas := addFace(t, s, photo, 0.9, bea)

	if err := s.RemoveFaces(t.Context(), ana, []int64{stranger.ID, beas.ID}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]int64{stranger.ID: 0, kept.ID: ana, beas.ID: bea} {
		if f, err := s.Face(t.Context(), id); err != nil || f.Person != want {
			t.Errorf("face %d belongs to %d (%v), want %d", id, f.Person, err, want)
		}
	}
	if got, want := exclusions(t, s), []string{fmt.Sprintf("%d:%d", stranger.ID, ana)}; !slices.Equal(got, want) {
		t.Errorf("exclusions = %v, want %v: Bea's face was never Ana's", got, want)
	}

	if err := s.AssignFaces(t.Context(), ana, []int64{stranger.ID}); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Face(t.Context(), stranger.ID); f.Person != 0 {
		t.Errorf("AssignFaces put a face back under the person it was taken out of: %d", f.Person)
	}
	if err := s.AssignFaces(t.Context(), bea, []int64{stranger.ID}); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.Face(t.Context(), stranger.ID); f.Person != bea {
		t.Errorf("a face taken out of Ana could not join Bea: %d", f.Person)
	}
	if err := s.RemoveFaces(t.Context(), bea, []int64{stranger.ID}); err != nil {
		t.Fatal(err)
	}
	if got := exclusions(t, s); len(got) != 2 {
		t.Errorf("exclusions after a second removal = %v, want one per person", got)
	}
	if err := s.RemoveFaces(t.Context(), ana, nil); err != nil {
		t.Errorf("RemoveFaces of nothing = %v", err)
	}
}

func facesMergeExclusions(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	from, into := newPerson(t, s, "Ana"), newPerson(t, s, "Ana María")
	onlyFrom := addFace(t, s, photo, 0.9, from)
	both := addFace(t, s, photo, 0.9, from)
	if err := s.RemoveFaces(t.Context(), from, []int64{onlyFrom.ID, both.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignFaces(t.Context(), into, []int64{both.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFaces(t.Context(), into, []int64{both.ID}); err != nil {
		t.Fatal(err)
	}

	if err := s.MergePeople(t.Context(), from, into); err != nil {
		t.Fatal(err)
	}
	want := []string{fmt.Sprintf("%d:%d", onlyFrom.ID, into), fmt.Sprintf("%d:%d", both.ID, into)}
	if got := exclusions(t, s); !slices.Equal(got, want) {
		t.Errorf("exclusions after a merge = %v, want %v", got, want)
	}
}

func facesExclusionsCascade(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	scanned(t, s, photo, db.EmbeddingMade, time.Now())
	ana, bea := newPerson(t, s, ""), newPerson(t, s, "Bea")
	gone := addFace(t, s, photo, 0.9, bea)
	unnamed := addFace(t, s, photo, 0.9, ana)
	if err := s.RemoveFaces(t.Context(), bea, []int64{gone.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFaces(t.Context(), ana, []int64{unnamed.ID}); err != nil {
		t.Fatal(err)
	}

	if n, err := s.DeleteEmptyPeople(t.Context()); err != nil || n != 1 {
		t.Fatalf("DeleteEmptyPeople = %d, %v", n, err)
	}
	if got, want := exclusions(t, s), []string{fmt.Sprintf("%d:%d", gone.ID, bea)}; !slices.Equal(got, want) {
		t.Errorf("exclusions after forgetting an unnamed person = %v, want %v", got, want)
	}
	if err := s.DeleteFacesOf(t.Context(), photo.ID); err != nil {
		t.Fatal(err)
	}
	if got := exclusions(t, s); len(got) != 0 {
		t.Errorf("exclusions after the photograph's faces went = %v", got)
	}

	again := addFace(t, s, photo, 0.9, bea)
	if err := s.RemoveFaces(t.Context(), bea, []int64{again.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetFaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := exclusions(t, s); len(got) != 0 {
		t.Errorf("exclusions after ForgetFaces = %v", got)
	}
}

func facesForget(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	scanned(t, s, photo, db.EmbeddingMade, time.Now())
	addFace(t, s, photo, 0.9, newPerson(t, s, "Ana"))

	if err := s.ForgetFaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	if people, err := s.People(t.Context()); err != nil || len(people) != 0 {
		t.Errorf("People after ForgetFaces = %v, %v", people, err)
	}
	if of, err := s.FacesOf(t.Context(), photo.ID); err != nil || len(of) != 0 {
		t.Errorf("FacesOf after ForgetFaces = %v, %v", faceIDs(of), err)
	}
	if ok, err := s.WantsFaceScan(t.Context(), photo.ID, faceModel, time.Now()); err != nil || !ok {
		t.Errorf("WantsFaceScan after ForgetFaces = %v, %v; want the photograph back", ok, err)
	}
}

func facesCounted(t *testing.T, s db.Repo) {
	made := photoAt(t, s, "made.jpg", time.Now())
	refused := photoAt(t, s, "refused.jpg", time.Now())
	photoAt(t, s, "waiting.jpg", time.Now())
	scanned(t, s, made, db.EmbeddingMade, time.Now())
	scanned(t, s, refused, db.EmbeddingRefused, time.Now())
	person := newPerson(t, s, "")
	newPerson(t, s, "")
	addFace(t, s, made, 0.9, person)
	addFace(t, s, made, 0.9, 0)

	c, err := s.FaceCounts(t.Context(), faceModel)
	if err != nil {
		t.Fatal(err)
	}
	if c != (db.FaceCounts{Files: 3, Scanned: 1, Refused: 1, Faces: 2, People: 1}) || c.Waiting() != 1 {
		t.Errorf("FaceCounts = %+v", c)
	}
}

// FacesOnAClosedStore asserts every face call answers an error once the
// store is closed: the error paths a driver test cannot otherwise reach.
func FacesOnAClosedStore(t *testing.T, store db.Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, now := t.Context(), time.Now()
	none := func(db.Face) error { return nil }
	calls := map[string]func() error{
		"PendingFaceScans": func() error { _, err := store.PendingFaceScans(ctx, faceModel, now, 1); return err },
		"WantsFaceScan":    func() error { _, err := store.WantsFaceScan(ctx, 1, faceModel, now); return err },
		"PutFaceScan":      func() error { return store.PutFaceScan(ctx, db.FaceScan{FileID: 1, At: now}) },
		"AddFace":          func() error { _, err := store.AddFace(ctx, db.Face{FileID: 1}); return err },
		"FacesOf":          func() error { _, err := store.FacesOf(ctx, 1); return err },
		"DeleteFacesOf":    func() error { return store.DeleteFacesOf(ctx, 1) },
		"Face":             func() error { _, err := store.Face(ctx, 1); return err },
		"EachFace":         func() error { return store.EachFace(ctx, faceModel, none) },
		"EachFaceCrop":     func() error { return store.EachFaceCrop(ctx, func(int64, string) error { return nil }) },
		"PersonFaces": func() error {
			_, err := store.PersonFaces(ctx, 1, db.FaceCursor{SortAt: now, ID: 1}, 1)
			return err
		},
		"People":            func() error { _, err := store.People(ctx); return err },
		"Person":            func() error { _, err := store.Person(ctx, 1); return err },
		"NewPerson":         func() error { _, err := store.NewPerson(ctx, ""); return err },
		"AssignFaces":       func() error { return store.AssignFaces(ctx, 1, []int64{1}) },
		"RenamePerson":      func() error { return store.RenamePerson(ctx, 1, "x") },
		"MergePeople":       func() error { return store.MergePeople(ctx, 1, 2) },
		"RemoveFaces":       func() error { return store.RemoveFaces(ctx, 1, []int64{1}) },
		"EachFaceExclusion": func() error { return store.EachFaceExclusion(ctx, func(int64, int64) error { return nil }) },
		"DeleteEmptyPeople": func() error { _, err := store.DeleteEmptyPeople(ctx); return err },
		"ForgetFaces":       func() error { return store.ForgetFaces(ctx) },
		"FaceCounts":        func() error { _, err := store.FaceCounts(ctx, faceModel); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s against a closed store reported no error", name)
		}
	}
}

// facesFnErrors is what an EachFace or EachFaceCrop callback's error does:
// it stops the walk and comes back as it is.
func facesFnErrors(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	scanned(t, s, photo, db.EmbeddingMade, time.Now())
	addFace(t, s, photo, 0.9, 0)
	stop := errors.New("stop")
	if err := s.EachFace(t.Context(), faceModel, func(db.Face) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("EachFace = %v, want the callback's error", err)
	}
	if err := s.EachFaceCrop(t.Context(), func(int64, string) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("EachFaceCrop = %v, want the callback's error", err)
	}
	person := newPerson(t, s, "")
	face := addFace(t, s, photo, 0.9, person)
	if err := s.RemoveFaces(t.Context(), person, []int64{face.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.EachFaceExclusion(t.Context(), func(int64, int64) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("EachFaceExclusion = %v, want the callback's error", err)
	}
}
