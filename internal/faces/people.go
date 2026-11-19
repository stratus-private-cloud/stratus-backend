package faces

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/storage"
)

// MaxName is the longest name a person is given, in characters: what the
// narrowest of the three engines' columns holds.
const MaxName = 255

// Names is the one segment each person is addressed by: their name when it is
// theirs alone, their name and number when somebody else has it too, and a
// number when they have none. A page and a folder are found by it, so it is
// one segment and never a slash.
func Names(people []db.Person) map[int64]string {
	taken := map[string]int{}
	for _, p := range people {
		if p.Name != "" {
			taken[strings.ToLower(p.Name)]++
		}
	}
	out := make(map[int64]string, len(people))
	for _, p := range people {
		id := strconv.FormatInt(p.ID, 10)
		switch {
		case p.Name == "":
			out[p.ID] = "Person " + id
		case taken[strings.ToLower(p.Name)] > 1:
			out[p.ID] = p.Name + " (" + id + ")"
		default:
			out[p.ID] = p.Name
		}
	}
	return out
}

// Resolve is the person a segment of Names names, or db.ErrNotFound.
func Resolve(people []db.Person, segment string) (db.Person, error) {
	names := Names(people)
	for _, p := range people {
		if names[p.ID] == segment {
			return p, nil
		}
	}
	return db.Person{}, fmt.Errorf("person %q: %w", segment, db.ErrNotFound)
}

// CleanName is a name as it is kept, or an error saying why it cannot be:
// spaces trimmed, no slash, since it is a segment, and no longer than MaxName.
func CleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	switch {
	case strings.ContainsAny(name, "/\\"):
		return "", errors.New("a name cannot have a slash in it")
	case utf8.RuneCountInString(name) > MaxName:
		return "", fmt.Errorf("a name is at most %d characters", MaxName)
	case name == "." || name == "..":
		return "", fmt.Errorf("%q is not a name", name)
	}
	return name, nil
}

// People is everybody with a face, for the pages.
func (s *Service) People(ctx context.Context) ([]db.Person, error) {
	all, err := s.store.People(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, p := range all {
		if p.Faces > 0 {
			out = append(out, p)
		}
	}
	return out, nil
}

// PersonFaces is a page of one person's faces.
func (s *Service) PersonFaces(ctx context.Context, person int64, after db.FaceCursor, limit int) ([]db.Face, error) {
	return s.store.PersonFaces(ctx, person, after, limit)
}

// FacesOf is the faces found in the bytes f points at now.
func (s *Service) FacesOf(ctx context.Context, f db.File) ([]db.Face, error) {
	all, err := s.store.FacesOf(ctx, f.ID)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, face := range all {
		if face.BlobKey == f.BlobKey {
			out = append(out, face)
		}
	}
	return out, nil
}

// Rename names a person, after CleanName.
func (s *Service) Rename(ctx context.Context, person int64, name string) error {
	name, err := CleanName(name)
	if err != nil {
		return err
	}
	return s.store.RenamePerson(ctx, person, name)
}

// Merge puts every face of from under into: the one correction grouping
// needs most, somebody who came out as two.
func (s *Service) Merge(ctx context.Context, from, into int64) error {
	if from == into {
		return nil
	}
	return s.store.Tx(ctx, func(r db.Repo) error { return r.MergePeople(ctx, from, into) })
}

// Remove takes faces out of person for good (#525): the other correction,
// somebody else's face in their folder. They are loose again, so the next
// round, which this makes due, can put them with whoever they belong to.
func (s *Service) Remove(ctx context.Context, person int64, faces []int64) error {
	if err := s.store.Tx(ctx, func(r db.Repo) error { return r.RemoveFaces(ctx, person, faces) }); err != nil {
		return err
	}
	s.mu.Lock()
	s.found += len(faces)
	s.mu.Unlock()
	return nil
}

// Crop is the picture of one face.
func (s *Service) Crop(ctx context.Context, face int64) (io.ReadCloser, int64, error) {
	f, err := s.store.Face(ctx, face)
	if err != nil {
		return nil, 0, err
	}
	body, info, err := s.blobs.Get(ctx, CropKey(f.BlobKey, f.ID), storage.Range{})
	if err != nil {
		return nil, 0, err
	}
	return body, info.Size, nil
}

// State is why the models are not ready, and empty once they are or when the
// models cannot say.
func (s *Service) State() string {
	if st, ok := s.models.(interface{ State() string }); ok {
		return st.State()
	}
	return ""
}
