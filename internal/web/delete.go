package web

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
)

// deletePrefix is where a delete is confirmed. The page stays now that there
// is a trash bin behind it (#274): deleting a folder is still worth a question,
// and the answer it gives has changed from "this cannot be undone" to how long
// you have to change your mind.
const deletePrefix = "/delete/"

func (h *handler) deleteForm(w http.ResponseWriter, r *http.Request, user string) {
	target, f, ok := h.targetOf(w, r, user)
	if !ok {
		return
	}
	v := view{
		Title: "Delete", User: user,
		Name: path.Base(target), IsDir: f.IsDir,
		Action: link(deletePrefix, target), Back: href(db.ParentOf(target)),
		Kept: h.keptDays(),
	}
	q := r.URL.Query()
	if c := q.Get(cancelParam); c != "" {
		v.Back = safeNext(c)
	}
	if n := q.Get(nextParam); n != "" {
		v.Next = safeNext(n)
	}
	h.render(w, http.StatusOK, pageDelete, v)
}

// nextParam and cancelParam are a delete asked from somewhere other than the
// file's folder -- the gallery -- whose folder is the wrong place to land:
// where to go once it is gone, and where Cancel goes. Both pass through
// safeNext, so neither is a redirect off this server.
const (
	nextParam   = "next"
	cancelParam = "cancel"
)

// deleteFrom is the delete page for a file asked about from another page.
func deleteFrom(p, next, cancel string) string {
	return link(deletePrefix, p) + "?" + url.Values{nextParam: {next}, cancelParam: {cancel}}.Encode()
}

// remove deletes a file, or a directory and everything under it.
func (h *handler) remove(w http.ResponseWriter, r *http.Request, user string) {
	target, _, ok := h.targetOf(w, r, user)
	if !ok {
		return
	}
	if err := h.files.Remove(r.Context(), user, target); err != nil {
		h.fail(w, r, user, err)
		return
	}
	if n := r.PostFormValue(nextParam); n != "" {
		redirectLocal(w, r, safeNext(n))
		return
	}
	redirectLocal(w, r, href(db.ParentOf(target)))
}

// selectedPrefix is where the listing's checkboxes post (#295), under the
// folder they were ticked in. The first post is the question and the second,
// which carries confirm, is the answer: a POST both times because five
// hundred names do not fit in a URL with any certainty.
//
// Each name is a deletion of its own, exactly as if it had been deleted from
// its row. One deletion for the lot would be one with several roots, which
// restoring, the trash page and /trash/ all assume never happens.
const selectedPrefix = "/delete-selected/"

func (h *handler) removeSelected(w http.ResponseWriter, r *http.Request, user string) {
	dir, err := toPath(r.PathValue("path"))
	if err != nil {
		h.fail(w, r, user, err)
		return
	}
	if err = r.ParseForm(); err != nil {
		h.badRequest(w, user, "That is not a selection.")
		return
	}
	names := r.PostForm["name"]
	targets := make([]string, 0, len(names))
	for _, name := range names {
		target, perr := toPath(path.Join(dir, name))
		if perr != nil || name == "" || strings.Contains(name, "/") || db.ParentOf(target) != dir {
			h.badRequest(w, user, "That is not a selection in this folder.")
			return
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		redirectLocal(w, r, href(dir))
		return
	}

	if r.PostForm.Get("confirm") == "" {
		h.render(w, http.StatusOK, pageDelete, view{
			Title: "Delete", User: user,
			Names: names, Action: link(selectedPrefix, dir), Back: href(dir),
			Kept: h.keptDays(),
		})
		return
	}
	for _, target := range targets {
		// Already gone is what was asked for: a second tab, or the button
		// pressed twice.
		if err = h.files.Remove(r.Context(), user, target); err != nil && !errors.Is(err, db.ErrNotFound) {
			h.fail(w, r, user, err)
			return
		}
	}
	redirectLocal(w, r, href(dir))
}
