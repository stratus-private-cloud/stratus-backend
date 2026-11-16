package faces_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/faces"
)

// source is the tree's three reads in memory.
type source struct {
	people []db.Person
	faces  []db.Face
	files  map[int64]db.File
	fail   string
}

var errSource = errors.New("broken")

func (s *source) People(context.Context) ([]db.Person, error) {
	if s.fail == "People" {
		return nil, errSource
	}
	return s.people, nil
}

func (s *source) PersonFaces(_ context.Context, person int64, after db.FaceCursor, limit int) ([]db.Face, error) {
	if s.fail == "PersonFaces" {
		return nil, errSource
	}
	var out []db.Face
	for _, f := range s.faces {
		if f.Person == person && (after.ID == 0 || f.ID > after.ID) && len(out) < limit {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s *source) FilesByID(_ context.Context, _ string, ids []int64) ([]db.File, error) {
	if s.fail == "FilesByID" {
		return nil, errSource
	}
	var out []db.File
	for _, id := range ids {
		if f, ok := s.files[id]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func newSource() *source {
	s := &source{
		people: []db.Person{{ID: 1, Name: "Ana", Faces: 4}, {ID: 2, Faces: 1}, {ID: 3, Name: "Gone"}},
		files: map[int64]db.File{
			10: {ID: 10, Path: "2024/IMG_0001.jpg"}, 11: {ID: 11, Path: "2025/img_0001.JPG"},
			12: {ID: 12, Path: "a/b.jpg"}, 13: {ID: 13, Path: "c.jpg"},
		},
	}
	s.faces = []db.Face{
		{ID: 1, FileID: 10, Person: 1}, {ID: 2, FileID: 10, Person: 1},
		{ID: 3, FileID: 11, Person: 1}, {ID: 4, FileID: 12, Person: 1}, {ID: 5, FileID: 13, Person: 2},
	}
	return s
}

func names(nodes []faces.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Path()
	}
	slices.Sort(out)
	return out
}

func TestThePeopleAreFoldersOfTheirPhotographs(t *testing.T) {
	tree := faces.NewTree(newSource(), "edu", true)
	root, err := tree.Resolve(t.Context(), "/")
	if err != nil || !root.Dir {
		t.Fatalf("Resolve(/) = %+v, %v", root, err)
	}
	people, err := tree.Children(t.Context(), root)
	if err != nil || !slices.Equal(names(people), []string{"Ana", "Person 2"}) {
		t.Fatalf("the root lists %v, %v; want the two with faces", names(people), err)
	}

	ana, err := tree.Resolve(t.Context(), "Ana/")
	if err != nil || !ana.Dir || ana.Path() != "Ana" {
		t.Fatalf("Resolve(Ana) = %+v, %v", ana, err)
	}
	photos, err := tree.Children(t.Context(), ana)
	// Two faces in one photograph are one photograph, and two names a
	// case-folding client would take for one are told apart.
	want := []string{"Ana/IMG_0001.jpg", "Ana/b.jpg", "Ana/img_0001 (2).JPG"}
	if err != nil || !slices.Equal(names(photos), want) {
		t.Fatalf("Ana holds %v, %v; want %v", names(photos), err, want)
	}

	leaf, err := tree.Resolve(t.Context(), "Ana/b.jpg")
	if err != nil || leaf.Dir || leaf.File.ID != 12 {
		t.Errorf("Resolve(Ana/b.jpg) = %+v, %v", leaf, err)
	}
	if kids, _ := tree.Children(t.Context(), leaf); kids != nil {
		t.Errorf("a photograph has children: %v", kids)
	}
	for _, missing := range []string{"Gone", "Nobody", "Ana/c.jpg", "Ana/b.jpg/x"} {
		if _, err := tree.Resolve(t.Context(), missing); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Resolve(%s) = %v, want ErrNotExist", missing, err)
		}
	}
}

func TestPeopleOffAreAnEmptyFolder(t *testing.T) {
	tree := faces.NewTree(newSource(), "edu", false)
	root, _ := tree.Resolve(t.Context(), "")
	if kids, err := tree.Children(t.Context(), root); err != nil || len(kids) != 0 {
		t.Errorf("with faces off the root lists %v, %v", names(kids), err)
	}
	if _, err := tree.Resolve(t.Context(), "Ana"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("with faces off Ana is %v", err)
	}
}

func TestATreeThatCannotReadSaysSo(t *testing.T) {
	for _, method := range []string{"People", "PersonFaces", "FilesByID"} {
		s := newSource()
		s.fail = method
		tree := faces.NewTree(s, "edu", true)
		_, err := tree.Resolve(t.Context(), "Ana/b.jpg")
		if method == "People" {
			if _, cerr := tree.Children(t.Context(), faces.Node{Dir: true}); cerr == nil {
				t.Error("the root listed with People broken")
			}
		}
		if !errors.Is(err, errSource) {
			t.Errorf("with %s broken Resolve = %v", method, err)
		}
		if method != "People" {
			if _, err := tree.Children(t.Context(), faces.Node{Person: db.Person{ID: 1}, Name: "Ana", Folder: "Ana", Dir: true}); err == nil {
				t.Errorf("with %s broken a folder listed", method)
			}
		}
	}
}
