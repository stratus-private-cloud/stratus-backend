// The backup page (#337). Everything this page runs is in this directory and
// is loaded by this page alone; see backup.go for why it is served from here.
//
// The folders are remembered as the handles the browser gave out, so coming
// back needs no picking -- only, where the browser has not kept the grant,
// a click to give it back, since asking for one needs a gesture.

import { open, store } from "./db.js";
import { PUT_LIMIT, href, list, ensure, put, tus } from "./remote.js";
import { backUp, fatal } from "./run.js";

// DEST is where a new folder goes, beside the app's phone_backup.
const DEST = "desktop_backup";
const READ = { mode: "read" };

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
  start();
}

async function start() {
  const db = await open();
  const folders = store(db, "folders");
  const settled = store(db, "settled");
  const uploads = store(db, "uploads");
  const server = {
    list,
    ensure,
    send: (path, file, key, progress) =>
      file.size <= PUT_LIMIT ? put(path, file) : tus(path, file, key, uploads, progress),
  };

  const listing = panel.querySelector("[data-backup-folders]");
  const row = panel.querySelector("template[data-backup-folder]");
  const busy = panel.querySelector("[data-backup-busy]");
  const rows = new Map();
  let running = false;

  async function render() {
    rows.clear();
    listing.replaceChildren();
    for (const folder of await folders.all()) {
      const li = row.content.firstElementChild.cloneNode(true);
      const el = (name) => li.querySelector(`[data-${name}]`);
      el("name").textContent = folder.name;
      el("dest").textContent = folder.dest;
      el("dest").href = href(folder.dest) + "/";
      el("allow").addEventListener("click", async () => {
        if ((await folder.handle.requestPermission(READ)) === "granted") {
          el("allow").classList.add("d-none");
          runAll();
        }
      });
      el("forget").addEventListener("click", async () => {
        if (!confirm(`Stop backing up ${folder.name}? Nothing is deleted, here or on the server.`)) {
          return;
        }
        await folders.del(folder.id);
        await settled.delPrefix(folder.id + "/");
        await uploads.delPrefix(folder.id + "/");
        render();
      });
      if (folder.last) {
        say(li, summary(folder.last), folder.last);
      }
      rows.set(folder.id, li);
      listing.append(li);
    }
  }

  async function runAll() {
    if (running) {
      return;
    }
    running = true;
    try {
      // One tab at a time: two passes over one folder would send the same
      // large file twice.
      if (navigator.locks) {
        await navigator.locks.request("stratus-backup", { ifAvailable: true }, async (lock) => {
          busy.classList.toggle("d-none", lock !== null);
          if (lock) {
            await passes();
          }
        });
      } else {
        await passes();
      }
    } finally {
      running = false;
    }
  }

  async function passes() {
    for (const folder of await folders.all()) {
      const li = rows.get(folder.id);
      if (!li) {
        continue;
      }
      if ((await folder.handle.queryPermission(READ)) !== "granted") {
        li.querySelector("[data-allow]").classList.remove("d-none");
        say(li, "This browser needs to be allowed to read it again.");
        continue;
      }
      try {
        const tally = await backUp(folder, { server, settled, report: (t) => progress(li, t) });
        const last = {
          at: Date.now(),
          seen: tally.seen,
          sent: tally.sent,
          here: tally.here,
          conflicts: tally.conflicts,
          failed: tally.failed,
        };
        await folders.put({ ...folder, last });
        say(li, summary(last), last);
      } catch (err) {
        say(li, failure(err));
        if (fatal(err)) {
          return;
        }
      }
    }
  }

  panel.querySelector("[data-backup-add]").addEventListener("click", async () => {
    let handle;
    try {
      handle = await window.showDirectoryPicker({ id: "stratus-backup", mode: "read" });
    } catch (err) {
      if (err.name === "AbortError") {
        return;
      }
      throw err;
    }
    const all = await folders.all();
    for (const folder of all) {
      if (await folder.handle.isSameEntry(handle)) {
        return runAll();
      }
    }
    const taken = new Set(all.map((f) => f.dest));
    let dest = `${DEST}/${handle.name}`;
    for (let n = 2; taken.has(dest); n++) {
      dest = `${DEST}/${handle.name} (${n})`;
    }
    await folders.put({ name: handle.name, handle, dest });
    await render();
    runAll();
  });
  panel.querySelector("[data-backup-run]").addEventListener("click", runAll);

  await render();
  runAll();
}

function progress(li, tally) {
  const bar = li.querySelector("[data-progress]");
  const now = tally.current;
  bar.classList.toggle("d-none", !now);
  let text = `${count(tally.seen, "file")} looked at, ${tally.sent} copied`;
  if (now) {
    bar.max = now.size || 1;
    bar.value = now.sent;
    text += ` — sending ${now.path}`;
  }
  li.querySelector("[data-state]").textContent = text;
}

// say puts a line under a folder, and the files a pass left alone or could
// not send under that.
function say(li, text, last) {
  li.querySelector("[data-progress]").classList.add("d-none");
  li.querySelector("[data-state]").textContent = text;
  const problems = li.querySelector("[data-problems]");
  const items = last
    ? [
        ...last.conflicts.map((path) => `${path}: a different file of that name is already on the server`),
        ...last.failed.map((f) => `${f.path}: ${f.error}`),
      ]
    : [];
  problems.classList.toggle("d-none", items.length === 0);
  problems.querySelector("summary").textContent = `${count(items.length, "file")} not copied`;
  problems.querySelector("ul").replaceChildren(
    ...items.map((item) => {
      const entry = document.createElement("li");
      entry.textContent = item;
      return entry;
    }),
  );
}

function summary(last) {
  let text = `${count(last.seen, "file")}: ${last.sent} copied, ${last.here} already backed up`;
  if (last.conflicts.length) {
    text += `, ${last.conflicts.length} in conflict`;
  }
  if (last.failed.length) {
    text += `, ${last.failed.length} failed`;
  }
  return `${text}. Last pass ${new Date(last.at).toLocaleString()}.`;
}

function failure(err) {
  if (err.status === 401 || err.status === 403) {
    return "Signed out. Sign in again and come back to this page.";
  }
  if (err.status === 507) {
    return "The server has no room left.";
  }
  if (err.name === "NotFoundError") {
    return "The folder is not where it was. Forget it and add it again.";
  }
  return `Stopped: ${err.message || err}`;
}

function count(n, noun) {
  return `${n.toLocaleString()} ${noun}${n === 1 ? "" : "s"}`;
}
