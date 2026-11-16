package faces

import (
	"cmp"
	"context"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// The people as folders (#519): /people/<name>/ holds every photograph a face
// of that person is in, wherever it was filed. The pages and the WebDAV mount
// are the same tree at the same addresses, as the photographs by date are, so
// what a person and a photograph are called is decided here and nowhere else.

// TreeSource is what the tree reads.
type TreeSource interface {
	People(ctx context.Context) ([]db.Person, error)
	PersonFaces(ctx context.Context, person int64, after db.FaceCursor, limit int) ([]db.Face, error)
	FilesByID(ctx context.Context, owner string, ids []int64) ([]db.File, error)
}

// treeBatch is how many faces one query reads while a person's folder is
// listed whole.
const treeBatch = 1000

// Node is one place in the tree: the root, a person, or a photograph of them.
type Node struct {
	Person db.Person
	// Name is what the node is called inside its parent: a person's segment,
	// or a photograph's name in their folder.
	Name   string
	Folder string
	Dir    bool
	File   db.File
}

// Path is the node's address under the mount, with no leading slash.
func (n Node) Path() string {
	if n.Folder == "" || n.Name == n.Folder && n.Dir {
		return n.Name
	}
	return n.Folder + "/" + n.Name
}

// Tree is the people of one owner, for the length of one request. Empty while
// faces are off: the folder is still there, as the root lists it, with
// nobody in it.
type Tree struct {
	source TreeSource
	owner  string
	on     bool

	people []db.Person
	names  map[int64]string
	photos map[int64]map[string]db.File
}

// NewTree makes the tree for owner; on is whether faces are.
func NewTree(source TreeSource, owner string, on bool) *Tree {
	return &Tree{source: source, owner: owner, on: on, photos: map[int64]map[string]db.File{}}
}

// Resolve turns a path under the mount into what is at it, or os.ErrNotExist.
func (t *Tree) Resolve(ctx context.Context, name string) (Node, error) {
	parts := strings.Split(strings.Trim(name, "/"), "/")
	if parts[0] == "" {
		return Node{Dir: true}, nil
	}
	if len(parts) > 2 {
		return Node{}, os.ErrNotExist
	}
	people, err := t.everybody(ctx)
	if err != nil {
		return Node{}, err
	}
	person, err := Resolve(people, parts[0])
	if err != nil {
		return Node{}, os.ErrNotExist
	}
	folder := Node{Person: person, Name: parts[0], Folder: parts[0], Dir: true}
	if len(parts) == 1 {
		return folder, nil
	}
	photos, err := t.Photos(ctx, person)
	if err != nil {
		return Node{}, err
	}
	f, ok := photos[parts[1]]
	if !ok {
		return Node{}, os.ErrNotExist
	}
	return Node{Person: person, Name: parts[1], Folder: parts[0], File: f}, nil
}

// Children lists the people, or the photographs of one.
func (t *Tree) Children(ctx context.Context, n Node) ([]Node, error) {
	if !n.Dir {
		return nil, nil
	}
	if n.Folder == "" {
		people, err := t.everybody(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]Node, 0, len(people))
		for _, p := range people {
			out = append(out, Node{Person: p, Name: t.names[p.ID], Folder: t.names[p.ID], Dir: true})
		}
		return out, nil
	}
	photos, err := t.Photos(ctx, n.Person)
	if err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(photos))
	for name, f := range photos {
		out = append(out, Node{Person: n.Person, Name: name, Folder: n.Folder, File: f})
	}
	return out, nil
}

// everybody is the people with a face, named by Names.
func (t *Tree) everybody(ctx context.Context) ([]db.Person, error) {
	if t.people != nil || !t.on {
		return t.people, nil
	}
	all, err := t.source.People(ctx)
	if err != nil {
		return nil, err
	}
	t.people = []db.Person{}
	for _, p := range all {
		if p.Faces > 0 {
			t.people = append(t.people, p)
		}
	}
	t.names = Names(t.people)
	return t.people, nil
}

// Photos reads every photograph one person is in and names them.
func (t *Tree) Photos(ctx context.Context, person db.Person) (map[string]db.File, error) {
	if named, ok := t.photos[person.ID]; ok {
		return named, nil
	}
	var ids []int64
	seen := map[int64]bool{}
	var after db.FaceCursor
	for {
		page, err := t.source.PersonFaces(ctx, person.ID, after, treeBatch)
		if err != nil {
			return nil, err
		}
		for _, f := range page {
			if !seen[f.FileID] {
				seen[f.FileID] = true
				ids = append(ids, f.FileID)
			}
		}
		if len(page) < treeBatch {
			break
		}
		last := page[len(page)-1]
		after = db.FaceCursor{SortAt: last.SortAt, ID: last.ID}
	}
	files, err := t.source.FilesByID(ctx, t.owner, ids)
	if err != nil {
		return nil, err
	}
	named := PhotoNames(files)
	t.photos[person.ID] = named
	return named, nil
}

// PhotoNames names a person's photographs after their files, in id order so
// that a name is stable, and without two that a case-folding client would
// take for one: internal/timeline's rule.
func PhotoNames(files []db.File) map[string]db.File {
	byID := slices.Clone(files)
	slices.SortFunc(byID, func(a, b db.File) int { return cmp.Compare(a.ID, b.ID) })
	named := make(map[string]db.File, len(byID))
	taken := make(map[string]bool, len(byID))
	for _, f := range byID {
		base := path.Base(f.Path)
		name := base
		for n := 2; taken[strings.ToLower(name)]; n++ {
			name = db.CopyName(base, n)
		}
		taken[strings.ToLower(name)] = true
		named[name] = f
	}
	return named
}
