// One pass over one folder: walk it, compare each file with what the server
// has, and send what is missing. It only ever adds (stratus-app#124):
//
//   - a file a pass has settled is never sent again, so one deleted on the
//     server stays deleted;
//   - a name the server already has with the same size is settled as it is,
//     which is also how an empty record is rebuilt by walking the server;
//   - a name the server has with another size is a conflict, reported and
//     left alone on both sides.
//
// Sizes rather than hashes: the server's ETag is a SHA-256, but Web Crypto
// cannot hash a stream, and reading every file a second time to tell two of
// the same size apart buys little for what it costs.

import { HTTPError } from "./remote.js";

// walk yields a folder one directory at a time, with that directory's files,
// so each directory on the server is listed once.
export async function* walk(dir, rel = "") {
  const files = [];
  const dirs = [];
  for await (const [name, handle] of dir.entries()) {
    (handle.kind === "file" ? files : dirs).push([name, handle]);
  }
  yield { rel, files };
  for (const [name, handle] of dirs) {
    yield* walk(handle, join(rel, name));
  }
}

export function join(...parts) {
  return parts.filter(Boolean).join("/");
}

// fatal is an answer no retry and no other file will change: the session has
// gone, or the server is out of room.
export function fatal(err) {
  return err instanceof HTTPError && [401, 403, 507].includes(err.status);
}

// transient is worth trying again: the network, or a server busy or restarting.
export function transient(err) {
  if (err instanceof HTTPError) {
    return err.status >= 500 || err.status === 429 || err.status === 409;
  }
  return err instanceof TypeError;
}

export async function retry(fn, { attempts = 5, base = 1000, sleep = wait } = {}) {
  for (let attempt = 0; ; attempt++) {
    try {
      return await fn();
    } catch (err) {
      if (attempt + 1 >= attempts || !transient(err)) {
        throw err;
      }
      await sleep(Math.min(60000, base * 2 ** attempt));
    }
  }
}

function wait(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// backUp runs one pass. server is { list, ensure, send }, settled is the
// store of that name, and report is called with the tally as it moves.
export async function backUp(folder, { server, settled, report = () => {}, retryOpts }) {
  const tally = { seen: 0, sent: 0, here: 0, conflicts: [], failed: [], current: null };
  const again = (fn) => retry(fn, retryOpts);

  for await (const { rel, files } of walk(folder.handle)) {
    const dest = join(folder.dest, rel);
    // Listed when a file first needs it, and made when a file first needs
    // that: a folder whose files are all settled costs no request at all.
    let there;
    let made = false;

    for (const [name, handle] of files) {
      const file = await handle.getFile();
      const path = join(rel, name);
      const key = folder.id + "/" + path;
      const record = { key, size: file.size, mtime: file.lastModified };
      tally.seen++;

      const was = await settled.get(key);
      if (was && was.size === file.size && was.mtime === file.lastModified) {
        tally.here++;
        report(tally);
        continue;
      }

      try {
        if (there === undefined) {
          const listed = await again(() => server.list(dest));
          made = listed !== null;
          there = listed ?? new Map();
        }
        const theirs = there.get(name);
        if (theirs) {
          if (!theirs.dir && theirs.size === file.size) {
            await settled.put(record);
            tally.here++;
          } else {
            tally.conflicts.push(path);
          }
          report(tally);
          continue;
        }

        if (!made) {
          await again(() => server.ensure(dest));
          made = true;
        }
        tally.current = { path, sent: 0, size: file.size };
        report(tally);
        const outcome = await again(() =>
          server.send(join(dest, name), file, key, (sent) => {
            tally.current = { path, sent, size: file.size };
            report(tally);
          }),
        );
        if (outcome === "conflict") {
          tally.conflicts.push(path);
        } else {
          await settled.put(record);
          tally.sent++;
        }
      } catch (err) {
        if (fatal(err)) {
          throw err;
        }
        tally.failed.push({ path, error: err.message || String(err) });
      }
      tally.current = null;
      report(tally);
    }
  }
  return tally;
}
