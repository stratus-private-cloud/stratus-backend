package db

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
)

// Faces is the faces found in photographs and the people they are grouped
// into (#519). Like Embeddings, the bytes of a vector are the caller's.
//
// Three tables: face_scans says what became of looking at a photograph, as
// embeddings does without the vector; faces holds what was found, many to a
// photograph; and people what they are grouped into. Nothing here groups:
// that is internal/faces, and these are the rows it reads and writes.
//
// A face carries the blob key of the bytes it was found in, because an
// overwrite keeps the file's id: a face whose key is not the file's any more is
// of a picture that is gone, and every query that shows one leaves it out.
type Faces interface {
	// PendingFaceScans is PendingEmbeddings for faces.
	PendingFaceScans(ctx context.Context, model string, staleBefore time.Time, limit int) ([]File, error)

	// WantsFaceScan is WantsEmbedding for faces.
	WantsFaceScan(ctx context.Context, fileID int64, model string, staleBefore time.Time) (bool, error)

	// PutFaceScan records what became of looking at one photograph, replacing
	// what was there, and answers ErrNotFound for a file with no media row.
	PutFaceScan(ctx context.Context, s FaceScan) error

	// AddFace records one face found and answers its id.
	AddFace(ctx context.Context, f Face) (int64, error)

	// FacesOf is every face recorded of a file, stale or not, oldest first.
	FacesOf(ctx context.Context, fileID int64) ([]Face, error)

	// DeleteFacesOf forgets every face of a file, before it is looked at again.
	DeleteFacesOf(ctx context.Context, fileID int64) error

	// Face is one face, stale or not, or ErrNotFound.
	Face(ctx context.Context, id int64) (Face, error)

	// EachFace hands fn every face model found in bytes a file still points
	// at, with its vector, which is what grouping is done over.
	EachFace(ctx context.Context, model string, fn func(Face) error) error

	// EachFaceCrop hands fn the id and blob key of every face recorded, stale
	// or not, which is what has a picture in the store.
	EachFaceCrop(ctx context.Context, fn func(id int64, blobKey string) error) error

	// PersonFaces is a page of one person's current faces, newest photograph
	// first, after the cursor when its ID is not zero.
	PersonFaces(ctx context.Context, person int64, after FaceCursor, limit int) ([]Face, error)

	// People is everybody, ordered by SortPeople, each with how many current
	// faces they have and the one that shows them best.
	People(ctx context.Context) ([]Person, error)

	// Person is one person, with or without faces, or ErrNotFound.
	Person(ctx context.Context, id int64) (Person, error)

	// NewPerson records somebody, named or not, and answers the id.
	NewPerson(ctx context.Context, name string) (int64, error)

	// AssignFaces puts the faces given that belong to nobody under person.
	// A face somebody already has is left where it is, which is what keeps a
	// grouping that runs again from undoing one made by hand, and so is a
	// face taken out of person (#525).
	AssignFaces(ctx context.Context, person int64, faces []int64) error

	// RemoveFaces takes the faces given that person has out of them and keeps
	// them out: they belong to nobody, and no AssignFaces puts them back
	// under person. A face person does not have is left alone.
	RemoveFaces(ctx context.Context, person int64, faces []int64) error

	// EachFaceExclusion hands fn every face and a person it was taken out of.
	EachFaceExclusion(ctx context.Context, fn func(face, person int64) error) error

	// RenamePerson names somebody; an empty name unnames them.
	RenamePerson(ctx context.Context, id int64, name string) error

	// MergePeople moves every face of from to into and forgets from, and a
	// face kept out of from is kept out of into: they are the same person.
	MergePeople(ctx context.Context, from, into int64) error

	// DeleteEmptyPeople forgets everybody without a name who has no faces
	// left, and answers how many.
	DeleteEmptyPeople(ctx context.Context) (int64, error)

	// ForgetFaces deletes every face, scan, person and exclusion: what
	// turning faces off means. The pictures of the faces are the caller's to
	// delete first.
	ForgetFaces(ctx context.Context) error

	// FaceCounts says how much of the library model has looked at, for
	// /status.
	FaceCounts(ctx context.Context, model string) (FaceCounts, error)
}

// FaceScan is what became of looking at one photograph for faces. State is
// EmbeddingMade for a photograph looked at, whether or not it had any, and
// EmbeddingRefused for one the model would not take.
type FaceScan struct {
	FileID  int64
	State   EmbeddingState
	Model   string
	BlobKey string
	At      time.Time
}

// Face is one face found in a photograph.
type Face struct {
	ID      int64
	FileID  int64
	BlobKey string
	// X, Y, W and H are the box around it, as fractions of the photograph
	// turned the right way up, so they hold at any size it is shown at.
	X, Y, W, H float32
	Score      float32
	Vector     []byte
	// Person is 0 for a face nobody has.
	Person int64
	// SortAt is the photograph's date, as the gallery orders by, which is what
	// a person's page is paged by.
	SortAt time.Time
}

// FaceCursor is where a page of a person's faces ended.
type FaceCursor struct {
	SortAt time.Time
	ID     int64
}

// Person is somebody the faces are grouped into.
type Person struct {
	ID   int64
	Name string
	// Faces is how many current faces they have, and Cover the one with the
	// best score, 0 for nobody.
	Faces int64
	Cover int64
}

// FaceCounts is how many photographs there are, how many model has looked at
// and how many it refused, and what it found.
type FaceCounts struct {
	Files   int64
	Scanned int64
	Refused int64
	Faces   int64
	People  int64
}

// Waiting is what no pass has reached yet.
func (c FaceCounts) Waiting() int64 { return c.Files - c.Scanned - c.Refused }

// SortPeople orders people as People answers them: named first, by name, and
// then the most faces first. Every driver sorts in Go so that the three agree
// on what a name's order is.
func SortPeople(people []Person) []Person {
	slices.SortStableFunc(people, func(a, b Person) int {
		switch {
		case (a.Name == "") != (b.Name == ""):
			if a.Name != "" {
				return -1
			}
			return 1
		case a.Name != b.Name:
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		case a.Faces != b.Faces:
			return cmp.Compare(b.Faces, a.Faces)
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return people
}
