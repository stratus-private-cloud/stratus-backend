package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/faces"
	"github.com/stratus-private-cloud/stratus-backend/internal/storage"
)

// peoplePrefix is the people the faces are grouped into (#519), one page each
// under the segment faces.Names gives them; facePrefix is the picture of one
// face.
const (
	peoplePrefix = "/people/"
	facePrefix   = "/face/"
)

// personPage is how many faces a person's page shows before the link to the
// next ones.
const personPage = 60

// facesFragment is what htmx asks for when it extends a person's faces.
const facesFragment = "faces"

// People is the faces as a page uses them.
type People interface {
	Counts(ctx context.Context) (db.FaceCounts, error)
	// State is why the models are not ready, and empty once they are.
	State() string
	People(ctx context.Context) ([]db.Person, error)
	PersonFaces(ctx context.Context, person int64, after db.FaceCursor, limit int) ([]db.Face, error)
	FacesOf(ctx context.Context, f db.File) ([]db.Face, error)
	Rename(ctx context.Context, person int64, name string) error
	Merge(ctx context.Context, from, into int64) error
	Crop(ctx context.Context, face int64) (io.ReadCloser, int64, error)
}

// personTile is one person on the people page, or in the viewer.
type personTile struct {
	Name  string
	Href  string
	Thumb string
	Faces int64
}

// peopleView is the people page and a person's.
type peopleView struct {
	// Off says faces are switched off, and Missing that they are on with
	// nothing to run them; either way there is nothing else to show.
	Off, Missing bool
	State        string
	People       []personTile
	// The rest is one person's page.
	Name, Segment string
	Unnamed       bool
	Faces         []tile
	More          string
	Others        []personTile
	Error         string
}

func personHref(segment string) string { return peoplePrefix + url.PathEscape(segment) }

func faceURL(id int64) string { return facePrefix + strconv.FormatInt(id, 10) }

// people is the page of everybody, and every person's page under it.
func (h *handler) people(w http.ResponseWriter, r *http.Request, user string) {
	ix := h.indexingNow()
	segment := strings.Trim(r.PathValue("path"), "/")
	pv := peopleView{Off: !ix.Faces, Missing: ix.Faces && ix.People == nil}
	if pv.Off || pv.Missing {
		if segment != "" {
			h.notFound(w, r)
			return
		}
		h.render(w, http.StatusOK, pagePeople, view{Title: "People", User: user, Gallery: "People", People: &pv})
		return
	}
	pv.State = ix.People.State()

	all, err := ix.People.People(r.Context())
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	names := faces.Names(all)
	if segment == "" {
		for _, p := range all {
			pv.People = append(pv.People, personTile{
				Name: names[p.ID], Href: personHref(names[p.ID]), Thumb: faceURL(p.Cover), Faces: p.Faces,
			})
		}
		h.render(w, http.StatusOK, pagePeople, view{Title: "People", User: user, Gallery: "People", People: &pv})
		return
	}

	if folder, name, ok := strings.Cut(segment, "/"); ok {
		// A photograph in somebody's folder is the file, which is also the
		// GET a WebDAV client makes of it.
		n, rerr := faces.NewTree(treeOf{ix.People, h.byID}, user, true).Resolve(r.Context(), folder+"/"+name)
		switch {
		case errors.Is(rerr, os.ErrNotExist):
			h.notFound(w, r)
		case rerr != nil:
			h.fail(w, r, user, rerr)
		default:
			h.download(w, r, user, n.File)
		}
		return
	}
	person, err := faces.Resolve(all, segment)
	if err != nil {
		h.notFound(w, r)
		return
	}
	h.person(w, r, user, ix.People, &pv, person, all, names)
}

// treeOf is the people pages' half of faces.TreeSource and the files by id
// the rest of the pages read.
type treeOf struct {
	people People
	files  interface {
		FilesByID(ctx context.Context, owner string, ids []int64) ([]db.File, error)
	}
}

func (t treeOf) People(ctx context.Context) ([]db.Person, error) { return t.people.People(ctx) }

func (t treeOf) PersonFaces(ctx context.Context, person int64, after db.FaceCursor, limit int) ([]db.Face, error) {
	return t.people.PersonFaces(ctx, person, after, limit)
}

func (t treeOf) FilesByID(ctx context.Context, owner string, ids []int64) ([]db.File, error) {
	return t.files.FilesByID(ctx, owner, ids)
}

func (h *handler) person(w http.ResponseWriter, r *http.Request, user string, people People, pv *peopleView,
	person db.Person, all []db.Person, names map[int64]string,
) {
	after, err := parseFaceCursor(r.URL.Query().Get("after"))
	if err != nil {
		h.badRequest(w, user, "That is not a place in this person's photographs.")
		return
	}
	found, err := people.PersonFaces(r.Context(), person.ID, after, personPage+1)
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	if len(found) > personPage {
		found = found[:personPage]
		last := found[len(found)-1]
		pv.More = personHref(names[person.ID]) + "?after=" + url.QueryEscape(encodeFaceCursor(last))
	}
	ids := make([]int64, len(found))
	for i, f := range found {
		ids[i] = f.FileID
	}
	photos, err := h.byID.FilesByID(r.Context(), user, ids)
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	paths := make(map[int64]string, len(photos))
	for _, f := range photos {
		paths[f.ID] = f.Path
	}
	for _, f := range found {
		if p, ok := paths[f.FileID]; ok {
			pv.Faces = append(pv.Faces, tile{Href: photoByFileHref(p), Thumb: faceURL(f.ID), Name: p})
		}
	}

	if r.Header.Get("HX-Request") == "true" {
		h.renderTemplate(w, http.StatusOK, pagePerson, facesFragment, view{People: pv})
		return
	}
	pv.Name, pv.Segment, pv.Unnamed = names[person.ID], names[person.ID], person.Name == ""
	for _, p := range all {
		if p.ID != person.ID {
			pv.Others = append(pv.Others, personTile{Name: names[p.ID], Href: names[p.ID]})
		}
	}
	h.render(w, http.StatusOK, pagePerson, view{Title: pv.Name, User: user, Gallery: "People", People: pv})
}

// changePerson is the two forms on a person's page: a name, and somebody else
// they are the same as.
func (h *handler) changePerson(w http.ResponseWriter, r *http.Request, user string) {
	ix := h.indexingNow()
	if !ix.Faces || ix.People == nil {
		h.notFound(w, r)
		return
	}
	all, err := ix.People.People(r.Context())
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	person, err := faces.Resolve(all, strings.Trim(r.PathValue("path"), "/"))
	if err != nil {
		h.notFound(w, r)
		return
	}

	switch r.PostFormValue("do") {
	case "rename":
		err = ix.People.Rename(r.Context(), person.ID, r.PostFormValue("name"))
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			h.badRequest(w, user, "That name cannot be used: "+err.Error()+".")
			return
		}
		person.Name, _ = faces.CleanName(r.PostFormValue("name"))
	case "merge":
		into, rerr := faces.Resolve(all, r.PostFormValue("into"))
		if rerr != nil {
			h.badRequest(w, user, "Choose who this person is.")
			return
		}
		err = ix.People.Merge(r.Context(), person.ID, into.ID)
		person = into
	default:
		h.badRequest(w, user, "Nothing was asked for.")
		return
	}
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	// Named by what they are called now, which a rename has just changed.
	for i := range all {
		if all[i].ID == person.ID {
			all[i].Name = person.Name
		}
	}
	redirectLocal(w, r, personHref(faces.Names(all)[person.ID]))
}

// face is the picture of one face. Immutable, since a face's id is never
// given to another.
func (h *handler) face(w http.ResponseWriter, r *http.Request, user string) {
	ix := h.indexingNow()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || ix.People == nil || !ix.Faces {
		http.NotFound(w, r)
		return
	}
	body, length, err := ix.People.Crop(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound), errors.Is(err, storage.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, user, err)
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = io.Copy(w, body)
}

// peopleIn is who is in f, for the viewer: everybody one of its faces has
// been put under, once each. A failure is logged and leaves the viewer as it
// was, as similarTo does.
func (h *handler) peopleIn(r *http.Request, f db.File) []personTile {
	ix := h.indexingNow()
	if ix.People == nil || !ix.Faces {
		return nil
	}
	found, err := ix.People.FacesOf(r.Context(), f)
	if err != nil || len(found) == 0 {
		if err != nil {
			slog.WarnContext(r.Context(), "reading who is in a photograph", "path", f.Path, "err", err)
		}
		return nil
	}
	all, err := ix.People.People(r.Context())
	if err != nil {
		slog.WarnContext(r.Context(), "reading who is in a photograph", "path", f.Path, "err", err)
		return nil
	}
	names := faces.Names(all)
	var out []personTile
	seen := map[int64]bool{}
	for _, face := range found {
		name, ok := names[face.Person]
		if face.Person == 0 || !ok || seen[face.Person] {
			continue
		}
		seen[face.Person] = true
		out = append(out, personTile{Name: name, Href: personHref(name), Thumb: faceURL(face.ID)})
	}
	return out
}

// encodeFaceCursor and parseFaceCursor carry a place in a person's faces
// through a URL, as the grid's cursor does: milliseconds and an id.
func encodeFaceCursor(f db.Face) string {
	return strconv.FormatInt(f.SortAt.UnixMilli(), 10) + "-" + strconv.FormatInt(f.ID, 10)
}

func parseFaceCursor(raw string) (db.FaceCursor, error) {
	if raw == "" {
		return db.FaceCursor{}, nil
	}
	ms, id, ok := strings.Cut(raw, "-")
	if !ok {
		return db.FaceCursor{}, errors.New("web: a cursor without an id")
	}
	at, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return db.FaceCursor{}, err
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return db.FaceCursor{}, errors.New("web: a cursor without an id")
	}
	return db.FaceCursor{SortAt: time.UnixMilli(at).UTC(), ID: n}, nil
}
