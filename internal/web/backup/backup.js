// The backup page (#337). Everything this page runs is in this directory and
// is loaded by this page alone; see backup.go for why it is served from here.

const unsupported = document.querySelector("[data-backup-unsupported]");
const panel = document.querySelector("[data-backup]");

// The one capability the page hangs on. A browser without it is told why
// rather than offered a button that does nothing.
if (typeof window.showDirectoryPicker !== "function") {
  unsupported.classList.remove("d-none");
} else {
  panel.classList.remove("d-none");
  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("/backup/sw.js");
  }
}
