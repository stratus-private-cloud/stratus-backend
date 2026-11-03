// Stand-ins for what a browser hands the backup page: directory handles, a
// store, and a server that answers fetch.

export function file(size, mtime = 1) {
  const bytes = new Uint8Array(size);
  return {
    kind: "file",
    getFile: async () => Object.assign(new Blob([bytes]), { lastModified: mtime }),
  };
}

export function dir(entries) {
  return {
    kind: "directory",
    entries: async function* () {
      yield* Object.entries(entries);
    },
  };
}

export function memory() {
  const rows = new Map();
  return {
    rows,
    get: async (key) => rows.get(key),
    put: async (value) => void rows.set(value.key, value),
    del: async (key) => void rows.delete(key),
    delPrefix: async (prefix) => {
      for (const key of [...rows.keys()]) {
        if (key.startsWith(prefix)) rows.delete(key);
      }
    },
  };
}

// fetcher answers each request with the first handler that returns a
// Response, and records every request it was asked.
export function fetcher(...handlers) {
  const calls = [];
  const fn = async (url, init = {}) => {
    const call = { url: String(url), method: init.method || "GET", headers: init.headers || {}, body: init.body };
    calls.push(call);
    for (const h of handlers) {
      const res = await h(call);
      if (res) return res;
    }
    throw new Error(`nothing answers ${call.method} ${call.url}`);
  };
  fn.calls = calls;
  return fn;
}
