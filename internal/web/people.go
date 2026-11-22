package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"slices"
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
	Remove(ctx context.Context, person int64, faces []int64) error
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

func personCoverURL(segment string, face int64) string {
	return personHref(segment) + "/?" + coverParam + "&" + faceParam + "=" + strconv.FormatInt(face, 10)
}

// bannerPeople is how many faces the top of the photos page shows.
const bannerPeople = 8

// banner is a few people for the top of the photos page: the named before
// the rest, each half shuffled so it is not the same faces every visit. A
// failure is no banner rather than no photos.
func (h *handler) banner(ctx context.Context) []personTile {
	ix := h.indexingNow()
	if !ix.Faces || ix.People == nil {
		return nil
	}
	all, err := ix.People.People(ctx)
	if err != nil {
		slog.WarnContext(ctx, "listing people for the photos page", "err", err)
		return nil
	}
	named := slices.DeleteFunc(slices.Clone(all), func(p db.Person) bool { return p.Name == "" })
	unnamed := slices.DeleteFunc(slices.Clone(all), func(p db.Person) bool { return p.Name != "" })
	shuffle(named)
	shuffle(unnamed)
	names := faces.Names(all)
	var out []personTile
	for _, p := range append(named, unnamed...) {
		if len(out) == bannerPeople {
			break
		}
		out = append(out, personTile{Name: names[p.ID], Href: personHref(names[p.ID]), Thumb: personCoverURL(names[p.ID], p.Cover)})
	}
	return out
}

func shuffle(people []db.Person) {
	rand.Shuffle(len(people), func(i, j int) { people[i], people[j] = people[j], people[i] }) //nolint:gosec // an order to look at, not a secret
}

// faceParam names the face a page draws as a person's cover, so the page's
// address changes when a merge changes the face and can be kept for good; an
// address without it is kept for days.
const faceParam = "face"

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
	if h.namedFace(w, r, user, ix.People, segment) {
		return
	}

	all, err := ix.People.People(r.Context())
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	names := faces.Names(all)
	if segment == "" {
		for _, p := range all {
			pv.People = append(pv.People, personTile{
				Name: names[p.ID], Href: personHref(names[p.ID]), Thumb: personCoverURL(names[p.ID], p.Cover), Faces: p.Faces,
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
	if r.URL.Query().Has(coverParam) {
		h.personCover(w, r, user, ix.People, person)
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
			pv.Faces = append(pv.Faces, tile{Href: photoByFileHref(p), Thumb: faceURL(f.ID), Name: p, Face: f.ID})
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

// changePerson is the three forms on a person's page: a name, somebody else
// they are the same as, and the faces that are not them.
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
	case "remove":
		var ids []int64
		for _, v := range r.PostForm["face"] {
			id, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil {
				h.badRequest(w, user, "That is not a face.")
				return
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			h.badRequest(w, user, "Tick the faces that are not this person.")
			return
		}
		err = ix.People.Remove(r.Context(), person.ID, ids)
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
	h.crop(w, r, user, ix.People, id, "private, max-age=31536000, immutable", "")
}

// personCover is a person's picture, the face their tiles show, at
// `?cover` on their own address: the page's to a browser and the folder's to a
// WebDAV client, which is how a client gets it without an address of ours, as
// an album's cover is got. Not immutable, because a merge or a face taken out
// changes which face it is: kept for days, or for good when the address names
// the face it is; the ETag is that face, so asking again is a 304.
func (h *handler) personCover(w http.ResponseWriter, r *http.Request, user string, people People, person db.Person) {
	face := strconv.FormatInt(person.Cover, 10)
	etag := `"face-` + face + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.crop(w, r, user, people, person.Cover, pictureCache, etag)
}

// namedFace answers a cover whose address names its face, which is every one
// a page draws, from that face alone: listing everybody to find whose it is
// cost a page of people a listing per picture. The face's bytes never change,
// so it is kept for good. Whether it is still the cover is the page's to
// know; a face that is gone falls back to asking the person.
func (h *handler) namedFace(w http.ResponseWriter, r *http.Request, user string, people People, segment string) bool {
	q := r.URL.Query()
	id, err := strconv.ParseInt(q.Get(faceParam), 10, 64)
	if !q.Has(coverParam) || err != nil || id <= 0 || segment == "" || strings.Contains(segment, "/") {
		return false
	}
	etag := `"face-` + strconv.FormatInt(id, 10) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	body, length, err := people.Crop(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound), errors.Is(err, storage.ErrNotFound):
		return false
	case err != nil:
		h.fail(w, r, user, err)
		return true
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	_, _ = io.Copy(w, body)
	return true
}

// crop answers a face's picture, and only a picture is kept: the cache and the
// validator go on once there are bytes (#554). A face's rows commit before its
// crop is stored, so a page drawn in between asks for one that is not there
// yet -- and a 404 carrying immutable was a hole that browser kept for a year.
func (h *handler) crop(w http.ResponseWriter, r *http.Request, user string, people People, id int64, cache, etag string) {
	body, length, err := people.Crop(r.Context(), id)
	switch {
	case errors.Is(err, db.ErrNotFound), errors.Is(err, storage.ErrNotFound):
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(w, r, user, err)
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Cache-Control", cache)
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
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
