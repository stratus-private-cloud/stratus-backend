// The server's half, in the protocols every other client uses (#337): WebDAV
// to see what is there and to make folders, PUT and tus to send a file. The
// session cookie goes with every request because they are all same-origin,
// which is the only reason this page needs no credentials of its own.

export const FILES = "/files/";
const TUS = "/tus/";
const TUS_VERSION = "1.0.0";

// PUT_LIMIT is the size above which a file goes by tus: a PUT that drops
// starts again from nothing, and a large one over a weak link never finishes.
export const PUT_LIMIT = 8 * 1024 * 1024;
const CHUNK = 8 * 1024 * 1024;

export class HTTPError extends Error {
  constructor(status, what) {
    super(`${what}: ${status}`);
    this.name = "HTTPError";
    this.status = status;
  }
}

// href is a tree path as a URL says it, one segment at a time.
export function href(path) {
  return FILES + path.split("/").filter(Boolean).map(encodeURIComponent).join("/");
}

const PROPFIND = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/><D:getcontentlength/></D:prop></D:propfind>`;

// list is a folder's children by name, or null when there is no such folder.
// Depth 1 and never infinity, which the server refuses (#160).
export async function list(path, fetchFn = fetch) {
  const self = href(path) + "/";
  const res = await fetchFn(self, {
    method: "PROPFIND",
    headers: { Depth: "1", "Content-Type": "application/xml; charset=utf-8" },
    body: PROPFIND,
  });
  if (res.status === 404) {
    return null;
  }
  if (res.status !== 207) {
    throw new HTTPError(res.status, `listing ${path}`);
  }
  const doc = new DOMParser().parseFromString(await res.text(), "application/xml");
  const children = new Map();
  for (const response of dav(doc, "response")) {
    const at = new URL(text(response, "href"), location.href).pathname;
    if (decodeURIComponent(at).replace(/\/$/, "") === decodeURIComponent(self).replace(/\/$/, "")) {
      continue;
    }
    const name = decodeURIComponent(at.replace(/\/$/, "").split("/").pop());
    const dir = dav(response, "collection").length > 0;
    children.set(name, { dir, size: dir ? 0 : Number(text(response, "getcontentlength")) });
  }
  return children;
}

function text(el, name) {
  const found = dav(el, name)[0];
  return found ? found.textContent.trim() : "";
}

// dav finds elements in the DAV: namespace by hand rather than with
// getElementsByTagNameNS, which happy-dom, where the tests run, answers
// with nothing for a prefixed name.
function dav(el, name) {
  return [...el.getElementsByTagName("*")].filter((e) => e.namespaceURI === "DAV:" && e.localName === name);
}

// ensure makes a folder and every one above it that is missing. 405 is
// MKCOL's answer for one that is already there.
export async function ensure(path, fetchFn = fetch) {
  const res = await fetchFn(href(path) + "/", { method: "MKCOL" });
  if (res.status === 201 || res.status === 405) {
    return;
  }
  const parent = path.split("/").slice(0, -1).join("/");
  if (res.status === 409 && parent) {
    await ensure(parent, fetchFn);
    return ensure(path, fetchFn);
  }
  throw new HTTPError(res.status, `making ${path}`);
}

// put sends a small file and never replaces one: If-None-Match is the server
// refusing to write over a name that was taken since the listing.
export async function put(path, file, fetchFn = fetch) {
  const res = await fetchFn(href(path), {
    method: "PUT",
    headers: { "If-None-Match": "*", "Content-Type": file.type || "application/octet-stream" },
    body: file,
  });
  if (res.status === 412) {
    return "conflict";
  }
  if (res.status !== 201 && res.status !== 204) {
    throw new HTTPError(res.status, `sending ${path}`);
  }
  return "sent";
}

// tus sends a large file in chunks and picks up where a previous attempt
// stopped, in this visit or an earlier one, from the address uploads kept.
//
// It has no If-None-Match: the upload replaces whatever is at the name when
// it completes, so between the listing and the last chunk a file written at
// that path by somebody else would be replaced. One user and one client
// writing into a backup's destination is why that was accepted (#337).
export async function tus(path, file, key, uploads, progress = () => {}, fetchFn = fetch) {
  const head = { "Tus-Resumable": TUS_VERSION };
  let upload = await uploads.get(key);
  if (upload && (upload.size !== file.size || upload.mtime !== file.lastModified)) {
    upload = null;
  }

  let offset = 0;
  if (upload) {
    const res = await fetchFn(upload.url, { method: "HEAD", headers: head });
    if (res.status === 404 || res.status === 410) {
      upload = null;
    } else if (!res.ok) {
      throw new HTTPError(res.status, `asking about ${path}`);
    } else {
      offset = Number(res.headers.get("Upload-Offset"));
    }
  }
  if (!upload) {
    const res = await fetchFn(TUS, {
      method: "POST",
      headers: {
        ...head,
        "Upload-Length": String(file.size),
        // A path from the origin, so it begins with files/ (#285).
        "Upload-Metadata": "filename " + base64(FILES.slice(1) + path),
      },
    });
    if (res.status !== 201) {
      throw new HTTPError(res.status, `starting ${path}`);
    }
    upload = { key, url: res.headers.get("Location"), size: file.size, mtime: file.lastModified };
    await uploads.put(upload);
  }

  while (offset < file.size) {
    progress(offset, file.size);
    const res = await fetchFn(upload.url, {
      method: "PATCH",
      headers: { ...head, "Upload-Offset": String(offset), "Content-Type": "application/offset+octet-stream" },
      body: file.slice(offset, offset + CHUNK),
    });
    if (res.status !== 204) {
      throw new HTTPError(res.status, `sending ${path}`);
    }
    offset = Number(res.headers.get("Upload-Offset"));
  }
  progress(file.size, file.size);
  await uploads.del(key);
  return "sent";
}

function base64(s) {
  let binary = "";
  for (const b of new TextEncoder().encode(s)) {
    binary += String.fromCharCode(b);
  }
  return btoa(binary);
}
