package web

import (
	"embed"
	"net/http"
	"net/url"
	"path"
)

// Backing a desktop folder up from the browser (#337): the one page in this UI
// that is a program rather than a document. It walks a folder the browser
// remembers, compares it with the tree over WebDAV and sends what is missing
// with PUT or tus, on the session the page already has -- nothing on this
// server knows it exists beyond the page, its scripts and its worker.
//
// **Its scripts are ES modules and live here rather than in /static/**,
// served under the build in the path: a module's imports are relative URLs, so
// the query string every other script here carries for its version would be
// lost one import down, and an immutable module that is not versioned is one a
// browser keeps after an upgrade.
//
// **It has a worker of its own, scoped to /backup/**, which is the root
// worker's file with its own prefix and its own list: what it precaches is
// these modules, which therefore never reach the shell every other page
// installs.
const (
	backupPrefix        = "/backup/"
	backupWorkerPath    = backupPrefix + "sw.js"
	backupModulesPrefix = backupPrefix + "js/"
	backupCache         = "backup:"
	// backupEntry is the module the page loads; the rest are its imports.
	backupEntry = "backup.js"
)

//go:embed backup/*.js
var backupFS embed.FS

// backupModules is every module in the binary, read once: the worker
// precaches all of them, since what one imports is fetched with no network
// otherwise.
var backupModules = func() []string {
	entries, err := backupFS.ReadDir("backup")
	if err != nil {
		panic("web: the backup modules are not in the binary: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}()

// backupModule is the address one module is served at for this build.
func (h *handler) backupModule(name string) string {
	return backupModulesPrefix + url.PathEscape(h.version) + "/" + name
}

func (h *handler) backupPage(w http.ResponseWriter, _ *http.Request, user string) {
	h.render(w, http.StatusOK, pageBackup, view{
		Title:   "Backup",
		User:    user,
		Modules: []string{h.backupModule(backupEntry)},
	})
}

// backupWorker is in front of the session for the root worker's reason: one
// that answered with the login redirect would fail to register.
func (h *handler) backupWorker(w http.ResponseWriter, _ *http.Request) {
	shell := h.shell()
	for _, name := range backupModules {
		shell = append(shell, h.backupModule(name))
	}
	h.writeWorker(w, backupCache, shell)
}

// backupScript serves a module of this build and of no other. The bytes under
// an address are immutable, so answering an old build's address with this
// build's bytes would plant them in a cache for a year.
func (h *handler) backupScript(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	body, err := backupFS.ReadFile(path.Join("backup", name))
	if r.PathValue("version") != h.version || path.Base(name) != name || err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(body)
}
