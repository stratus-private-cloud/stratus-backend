// What the backup page remembers between visits, in this browser's IndexedDB:
//
//   folders  -- the directory handles somebody picked, and where each goes
//   settled  -- every file a pass has accounted for, by folder and path
//   uploads  -- the tus address of a large file half sent
//
// settled only ever grows while its folder is kept, and that is the rule
// rather than bookkeeping: a file it names is never sent again, which is what
// keeps a file deleted on the server from coming back (stratus-app#124).

const NAME = "stratus-backup";
const VERSION = 1;

export function open(factory = indexedDB) {
  return new Promise((resolve, reject) => {
    const req = factory.open(NAME, VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      db.createObjectStore("folders", { keyPath: "id", autoIncrement: true });
      db.createObjectStore("settled", { keyPath: "key" });
      db.createObjectStore("uploads", { keyPath: "key" });
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function done(req) {
  return new Promise((resolve, reject) => {
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

// store wraps one object store with a transaction per call. Nothing here
// needs two writes to land together, and a transaction held across a fetch
// commits on its own halfway through.
export function store(db, name) {
  const os = (mode) => db.transaction(name, mode).objectStore(name);
  return {
    get: (key) => done(os("readonly").get(key)),
    all: () => done(os("readonly").getAll()),
    put: (value) => done(os("readwrite").put(value)),
    del: (key) => done(os("readwrite").delete(key)),
    // Every key that begins with prefix, which is how a folder's files are
    // keyed.
    delPrefix: (prefix) =>
      done(os("readwrite").delete(IDBKeyRange.bound(prefix, prefix + "￿"))),
  };
}
