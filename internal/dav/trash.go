package dav

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/stratus-private-cloud/stratus-backend/internal/auth"
	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/trash"
)

// What has been deleted, as a collection of its own (#323).
//
// The trash had two doors and both were inside the UI -- the page that asks
// before deleting, and the line on /status that counts what it holds -- so a
// client that mounts this server could not see it at all. It is a mount beside
// /photos/ and /playlists/ now, for the same reason those are mounts: this URL
// space is the server's, so nothing generated can collide with a real file.
//
// **One collection per deletion**, named by what was deleted, which is
// internal/trash's rule and shared with the pages. Class 1 with no LOCK.
//
// **Two writes, and each is a verb that already means it** (stratus-app#179):
// `DELETE` of a deletion destroys it now, the page's Destroy, and `MOVE` of
// one to an address under /files/ puts it back there, the page's Restore with
// the destination chosen by the client. Nothing else writes, and neither
// reaches inside a deletion: a batch is restored and destroyed whole, which is
// the one shape the port has, and one photograph comes out by being copied.
// A `MOVE` never replaces, whatever `Overwrite` says -- a taken destination
// is a 412 for the client to number around, since what has the name now is
// not a mistake -- and it is checked against no lock under /files/, whose
// table is that mount's: a restore is not a write a lock is held against.
//
// **This half answers the methods a browser never sends** (#279), like the
// other generated mounts: a GET of a deleted file is internal/web's and serves
// the blob that was never moved, which is what makes pulling one photograph
// out of an accident a drag in Finder rather than a restore of the whole
// folder. The mount itself is the generated one in generated.go (#359); what
// it says about a deleted file is the row exactly as it was, since nothing
// about the file changed when it was deleted.

// TrashService is what the trash mount reads and the two writes it answers.
type TrashService interface {
	trash.Source
	RestoreTo(ctx context.Context, owner, batch, to string) (db.File, error)
	DestroyTrashed(ctx context.Context, owner, batch string) (int, int64, error)
}

// trashMethods is generatedMethods and the two writes.
const trashMethods = generatedMethods + ", MOVE, DELETE"

// Trash serves the owner's deletions under prefix, and restores them under
// filesPrefix.
func Trash(prefix, filesPrefix string, service TrashService) http.Handler {
	read := mount[trash.Node]{
		what:  "the trash",
		open:  func(_ context.Context, owner string) tree[trash.Node] { return trash.New(service, owner) },
		entry: func(n trash.Node) entry { return trashEntry{n: n, files: filesPrefix} },
	}.handler(prefix)
	prefix = strings.TrimSuffix(prefix, "/")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodOptions:
			w.Header().Set("DAV", "1")
			w.Header().Set("Allow", trashMethods)
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodDelete, "MOVE":
		default:
			read.ServeHTTP(w, r)
			return
		}
		owner, ok := auth.User(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		name := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
		n, err := trash.New(service, owner).Resolve(r.Context(), name)
		if err == nil && !n.Deletion {
			// The root, or something inside a deletion: neither is a batch.
			http.Error(w, "only a whole deletion can be restored or destroyed", http.StatusForbidden)
			return
		}
		switch {
		case errors.Is(err, os.ErrNotExist):
			http.NotFound(w, r)
			return
		case err != nil:
			slog.ErrorContext(r.Context(), "dav: cannot read the trash", "err", err)
			http.Error(w, "the server could not read the trash", http.StatusInternalServerError)
			return
		}

		if r.Method == http.MethodDelete {
			if _, _, err := service.DestroyTrashed(r.Context(), owner, n.Row.Batch); err != nil {
				slog.ErrorContext(r.Context(), "dav: cannot destroy a deletion", "err", err)
				http.Error(w, "the server could not destroy it", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		to, ok := restoreTarget(r.Header.Get("Destination"), filesPrefix)
		if !ok {
			http.Error(w, "a deletion is restored to an address under "+filesPrefix, http.StatusBadGateway)
			return
		}
		switch _, err := service.RestoreTo(r.Context(), owner, n.Row.Batch, to); {
		case err == nil:
			w.WriteHeader(http.StatusCreated)
		case errors.Is(err, db.ErrConflict):
			// Taken, or a file where a folder on the way has to be: either
			// way the destination is the client's to change.
			http.Error(w, err.Error(), http.StatusPreconditionFailed)
		case errors.Is(err, db.ErrInvalidPath):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, db.ErrNotFound):
			http.NotFound(w, r)
		default:
			slog.ErrorContext(r.Context(), "dav: cannot restore a deletion", "err", err)
			http.Error(w, "the server could not restore it", http.StatusInternalServerError)
		}
	})
}

// restoreTarget is the path under filesPrefix a Destination names, absolute
// URI or path alike. RFC 4918 10.3 makes a destination on another server a
// 502, which is what anything outside the tree is answered with too: there is
// nowhere else here a deletion could go.
func restoreTarget(destination, filesPrefix string) (string, bool) {
	u, err := url.Parse(destination)
	if err != nil || destination == "" {
		return "", false
	}
	rest, ok := strings.CutPrefix(u.Path, filesPrefix)
	rest = strings.Trim(rest, "/")
	return rest, ok && rest != ""
}

// trashEntry is a node of the trash as the mount reads it.
type trashEntry struct {
	n     trash.Node
	files string
}

// DeletedFrom is where a deletion was taken from, under the tree: the address
// a client restores it to (stratus-app#179). Only a deletion has one -- what
// is inside it goes back with it -- and it is an address that may well be
// taken by now, which is why it is a separate property from source: that one
// names a file that is there.
func (e trashEntry) DeletedFrom() (string, bool) {
	if !e.n.Deletion {
		return "", false
	}
	return e.files + e.n.Row.Path, true
}

func (e trashEntry) Base() string  { return e.n.Name }
func (e trashEntry) IsDir() bool   { return e.n.Dir }
func (e trashEntry) File() db.File { return e.n.Row.File }
