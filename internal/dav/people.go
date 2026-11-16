package dav

import (
	"context"
	"net/http"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/faces"
)

// The people the faces are grouped into, as folders (#519): /people/Ana/
// holds every photograph Ana is in, wherever it was filed, as /photos/ holds
// them by date. Read-only, and naming a person or merging two is the page's.
//
// **This half answers the methods a browser never sends**, like the other
// generated mounts: the same addresses are the people pages to a browser, and
// a GET of a photograph is the file, served by internal/web. The names are
// internal/faces', which the pages use too.
//
// While faces are off the folder is there and empty, because the root lists
// it either way and a listing that named a folder it then refused would be a
// client's error to explain.

// People serves the owner's people under prefix; on says whether faces are.
func People(prefix, filesPrefix string, source faces.TreeSource, on func() bool) http.Handler {
	return mount[faces.Node]{
		what: "the people",
		open: func(_ context.Context, owner string) tree[faces.Node] {
			return faces.NewTree(source, owner, on())
		},
		entry: func(n faces.Node) entry { return personEntry{n} },
		files: filesPrefix,
	}.handler(prefix)
}

// personEntry is a node of the people as the mount reads it.
type personEntry struct{ n faces.Node }

func (e personEntry) Base() string  { return e.n.Name }
func (e personEntry) IsDir() bool   { return e.n.Dir }
func (e personEntry) File() db.File { return e.n.File }
