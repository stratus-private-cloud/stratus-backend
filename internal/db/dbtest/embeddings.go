package dbtest

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// RunEmbeddings executes the vector cases against the repository built by
// newRepo (#505).
func RunEmbeddings(t *testing.T, newRepo func(t *testing.T) db.Repo) {
	t.Helper()

	cases := []struct {
		name string
		fn   func(t *testing.T, s db.Repo)
	}{
		{"pending is the indexed photographs, newest first", embeddingsPending},
		{"a vector or a refusal takes a photograph out, and a refusal comes back stale", embeddingsVerdicts},
		{"another model or other bytes put a photograph back", embeddingsModelAndBytes},
		{"a re-index keeps the vector", embeddingsSurviveReindex},
		{"one file is asked about by the queue's rule", embeddingsWanted},
		{"a vector comes back byte for byte, for its model and its bytes only", embeddingsEach},
		{"the counts are the photographs, the vectors and the refusals", embeddingsCounted},
		{"a vector needs a media row, and goes with the file", embeddingsNeedARow},
		{"files by id are the owner's that are still there", embeddingsFilesByID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.fn(t, newRepo(t))
		})
	}
}

const embedModel = "siglip2-test"

func photoAt(t *testing.T, s db.Repo, path string, taken time.Time) db.File {
	t.Helper()
	f := put(t, s, file(path))
	m := indexed(f, 1)
	m.TakenAt = taken
	if err := s.PutMedia(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	return f
}

func vectorOf(seed byte) []byte {
	v := make([]byte, 772)
	for i := range v {
		v[i] = seed + byte(i)
	}
	return v
}

func embedded(f db.File, state db.EmbeddingState, model string, at time.Time) db.Embedding {
	e := db.Embedding{FileID: f.ID, State: state, Model: model, BlobKey: f.BlobKey, At: at}
	if state == db.EmbeddingMade {
		e.Vector = vectorOf(byte(f.ID)) //nolint:gosec // a pattern, not a number
	}
	return e
}

func pendingIDs(t *testing.T, s db.Repo, model string, staleBefore time.Time) []int64 {
	t.Helper()
	files, err := s.PendingEmbeddings(t.Context(), model, staleBefore, 10)
	if err != nil {
		t.Fatalf("PendingEmbeddings: %v", err)
	}
	ids := make([]int64, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	return ids
}

func putEmbedding(t *testing.T, s db.Repo, e db.Embedding) {
	t.Helper()
	if err := s.PutEmbedding(t.Context(), e); err != nil {
		t.Fatalf("PutEmbedding: %v", err)
	}
}

func embeddingsPending(t *testing.T, s db.Repo) {
	old := photoAt(t, s, "old.jpg", time.Date(2019, 3, 1, 9, 0, 0, 0, time.UTC))
	recent := photoAt(t, s, "recent.jpg", time.Date(2024, 8, 1, 9, 0, 0, 0, time.UTC))
	film := put(t, s, file("holiday.mp4"))
	if err := s.PutMedia(t.Context(), indexedAs(film, db.KindVideo, 1)); err != nil {
		t.Fatal(err)
	}
	// Nobody has indexed it, so nothing knows it is a photograph yet.
	put(t, s, file("unread.jpg"))

	got := pendingIDs(t, s, embedModel, time.Now())
	if len(got) != 2 || got[0] != recent.ID || got[1] != old.ID {
		t.Errorf("PendingEmbeddings = %v, want [%d %d]: the photographs, newest first", got, recent.ID, old.ID)
	}
}

func embeddingsVerdicts(t *testing.T, s db.Repo) {
	made := photoAt(t, s, "made.jpg", time.Now())
	refused := photoAt(t, s, "refused.jpg", time.Now())
	at := time.Now()
	putEmbedding(t, s, embedded(made, db.EmbeddingMade, embedModel, at))
	putEmbedding(t, s, embedded(refused, db.EmbeddingRefused, embedModel, at))

	if got := pendingIDs(t, s, embedModel, at.Add(-time.Hour)); len(got) != 0 {
		t.Errorf("PendingEmbeddings while both verdicts stand = %v, want none", got)
	}
	if got := pendingIDs(t, s, embedModel, at.Add(time.Hour)); len(got) != 1 || got[0] != refused.ID {
		t.Errorf("PendingEmbeddings once the refusal is stale = %v, want [%d]", got, refused.ID)
	}
}

func embeddingsModelAndBytes(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	putEmbedding(t, s, embedded(photo, db.EmbeddingMade, embedModel, time.Now()))
	if got := pendingIDs(t, s, "a-better-model", time.Now()); len(got) != 1 || got[0] != photo.ID {
		t.Errorf("PendingEmbeddings for another model = %v, want the photograph back", got)
	}

	replacement := file("photo.jpg")
	replacement.ETag = `"another photograph"`
	replacement.BlobKey = "blobs/photo-2"
	if second := put(t, s, replacement); second.ID != photo.ID {
		t.Fatalf("the overwrite made a new row; this case is about the one that does not")
	}
	if got := pendingIDs(t, s, embedModel, time.Now()); len(got) != 1 || got[0] != photo.ID {
		t.Errorf("PendingEmbeddings after an overwrite = %v, want the photograph back", got)
	}
}

func embeddingsSurviveReindex(t *testing.T, s db.Repo) {
	photo := photoAt(t, s, "photo.jpg", time.Now())
	putEmbedding(t, s, embedded(photo, db.EmbeddingMade, embedModel, time.Now()))
	if err := s.PutMedia(t.Context(), indexed(photo, 2)); err != nil {
		t.Fatal(err)
	}
	if got := pendingIDs(t, s, embedModel, time.Now()); len(got) != 0 {
		t.Errorf("PendingEmbeddings after a re-index = %v, want the vector kept", got)
	}
}

func wants(t *testing.T, s db.Repo, f db.File, model string, staleBefore time.Time) bool {
	t.Helper()
	ok, err := s.WantsEmbedding(t.Context(), f.ID, model, staleBefore)
	if err != nil {
		t.Fatalf("WantsEmbedding: %v", err)
	}
	return ok
}

func embeddingsWanted(t *testing.T, s db.Repo) {
	fresh := photoAt(t, s, "fresh.jpg", time.Now())
	made := photoAt(t, s, "made.jpg", time.Now())
	refused := photoAt(t, s, "refused.jpg", time.Now())
	film := put(t, s, file("holiday.mp4"))
	if err := s.PutMedia(t.Context(), indexedAs(film, db.KindVideo, 1)); err != nil {
		t.Fatal(err)
	}
	unread := put(t, s, file("unread.jpg"))
	at := time.Now()
	putEmbedding(t, s, embedded(made, db.EmbeddingMade, embedModel, at))
	putEmbedding(t, s, embedded(refused, db.EmbeddingRefused, embedModel, at))

	for _, c := range []struct {
		f     db.File
		model string
		stale time.Time
		want  bool
	}{
		{fresh, embedModel, at.Add(-time.Hour), true},
		{made, embedModel, at.Add(time.Hour), false},
		{made, "a-better-model", at.Add(-time.Hour), true},
		{refused, embedModel, at.Add(-time.Hour), false},
		{refused, embedModel, at.Add(time.Hour), true},
		{film, embedModel, at, false},
		{unread, embedModel, at, false},
	} {
		if got := wants(t, s, c.f, c.model, c.stale); got != c.want {
			t.Errorf("WantsEmbedding(%s, %s) = %v, want %v", c.f.Path, c.model, got, c.want)
		}
	}

	replacement := file("made.jpg")
	replacement.BlobKey = "blobs/made-2"
	put(t, s, replacement)
	if !wants(t, s, made, embedModel, at.Add(-time.Hour)) {
		t.Error("WantsEmbedding after an overwrite = false, want the photograph back")
	}
}

func eachOf(t *testing.T, s db.Repo, model string) map[int64][]byte {
	t.Helper()
	got := map[int64][]byte{}
	err := s.EachEmbedding(t.Context(), model, func(id int64, v []byte) error {
		got[id] = v
		return nil
	})
	if err != nil {
		t.Fatalf("EachEmbedding: %v", err)
	}
	return got
}

func embeddingsEach(t *testing.T, s db.Repo) {
	a := photoAt(t, s, "a.jpg", time.Now())
	b := photoAt(t, s, "b.jpg", time.Now())
	refused := photoAt(t, s, "refused.jpg", time.Now())
	other := photoAt(t, s, "other.jpg", time.Now())
	stale := photoAt(t, s, "stale.jpg", time.Now())
	for _, e := range []db.Embedding{
		embedded(a, db.EmbeddingMade, embedModel, time.Now()),
		embedded(b, db.EmbeddingMade, embedModel, time.Now()),
		embedded(refused, db.EmbeddingRefused, embedModel, time.Now()),
		embedded(other, db.EmbeddingMade, "another-model", time.Now()),
		embedded(stale, db.EmbeddingMade, embedModel, time.Now()),
	} {
		putEmbedding(t, s, e)
	}
	replacement := file("stale.jpg")
	replacement.BlobKey = "blobs/stale-2"
	put(t, s, replacement)

	got := eachOf(t, s, embedModel)
	if len(got) != 2 {
		t.Fatalf("EachEmbedding gave %d vectors, want the two of this model and these bytes", len(got))
	}
	for _, f := range []db.File{a, b} {
		if !bytes.Equal(got[f.ID], vectorOf(byte(f.ID))) { //nolint:gosec // a pattern, not a number
			t.Errorf("the vector of %s did not come back byte for byte", f.Path)
		}
	}

	stop := errors.New("enough")
	calls := 0
	err := s.EachEmbedding(t.Context(), embedModel, func(int64, []byte) error { calls++; return stop })
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("an error from fn = %v after %d calls, want it back after one", err, calls)
	}
}

func embeddingsCounted(t *testing.T, s db.Repo) {
	made := photoAt(t, s, "made.jpg", time.Now())
	refused := photoAt(t, s, "refused.jpg", time.Now())
	photoAt(t, s, "waiting.jpg", time.Now())
	other := photoAt(t, s, "other.jpg", time.Now())
	track := put(t, s, file("song.mp3"))
	if err := s.PutMedia(t.Context(), indexedAs(track, db.KindAudio, 1)); err != nil {
		t.Fatal(err)
	}
	putEmbedding(t, s, embedded(made, db.EmbeddingMade, embedModel, time.Now()))
	putEmbedding(t, s, embedded(refused, db.EmbeddingRefused, embedModel, time.Now()))
	putEmbedding(t, s, embedded(other, db.EmbeddingMade, "another-model", time.Now()))

	got, err := s.EmbeddingCounts(t.Context(), embedModel)
	if err != nil {
		t.Fatal(err)
	}
	want := db.EmbeddingCounts{Files: 4, Made: 1, Refused: 1}
	if got != want {
		t.Errorf("EmbeddingCounts = %+v, want %+v", got, want)
	}
	if got.Waiting() != 2 {
		t.Errorf("Waiting = %d, want 2: the photograph nobody read and the one another model did", got.Waiting())
	}
}

func embeddingsNeedARow(t *testing.T, s db.Repo) {
	unread := put(t, s, file("unread.jpg"))
	if err := s.PutEmbedding(t.Context(), embedded(unread, db.EmbeddingMade, embedModel, time.Now())); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("a vector for a file nothing has indexed = %v, want ErrNotFound", err)
	}

	photo := photoAt(t, s, "photo.jpg", time.Now())
	putEmbedding(t, s, embedded(photo, db.EmbeddingMade, embedModel, time.Now()))
	if err := s.DeleteFile(t.Context(), owner, "photo.jpg"); err != nil {
		t.Fatal(err)
	}
	if got := eachOf(t, s, embedModel); len(got) != 0 {
		t.Errorf("EachEmbedding after the file was deleted = %d vectors, want none", len(got))
	}
}

func embeddingsFilesByID(t *testing.T, s db.Repo) {
	a := put(t, s, file("a.jpg"))
	b := put(t, s, file("b.jpg"))
	gone := put(t, s, file("gone.jpg"))
	if err := s.DeleteFile(t.Context(), owner, "gone.jpg"); err != nil {
		t.Fatal(err)
	}
	theirs := file("theirs.jpg")
	theirs.OwnerID = "somebody-else"
	other := put(t, s, theirs)

	got, err := s.FilesByID(t.Context(), owner, []int64{b.ID, gone.ID, other.ID, a.ID})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, f := range got {
		paths[f.Path] = true
	}
	if len(got) != 2 || !paths["a.jpg"] || !paths["b.jpg"] {
		t.Errorf("FilesByID = %+v, want a.jpg and b.jpg alone", got)
	}
	if none, err := s.FilesByID(t.Context(), owner, nil); err != nil || len(none) != 0 {
		t.Errorf("FilesByID of nothing = %+v, %v", none, err)
	}
}
