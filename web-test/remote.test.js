import { describe, expect, test } from "vitest";
import { ensure, href, list, put, tus } from "../internal/web/backup/remote.js";
import { fetcher, memory } from "./fakes.js";

const multistatus = `<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/files/desktop_backup/Pictures/</D:href>
    <D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>
  <D:response><D:href>/files/desktop_backup/Pictures/caf%C3%A9%20%231.jpg</D:href>
    <D:propstat><D:prop><D:resourcetype/><D:getcontentlength>1234</D:getcontentlength></D:prop></D:propstat></D:response>
  <D:response><D:href>http://stratus.test/files/desktop_backup/Pictures/trip/</D:href>
    <D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>
</D:multistatus>`;

describe("list", () => {
  test("a folder's children by name, without the folder itself", async () => {
    const f = fetcher(() => new Response(multistatus, { status: 207 }));

    const children = await list("desktop_backup/Pictures", f);

    expect([...children]).toEqual([
      ["café #1.jpg", { dir: false, size: 1234 }],
      ["trip", { dir: true, size: 0 }],
    ]);
    expect(f.calls[0]).toMatchObject({ method: "PROPFIND", url: "/files/desktop_backup/Pictures/" });
    expect(f.calls[0].headers.Depth).toBe("1");
  });

  test("no such folder is null rather than an error", async () => {
    expect(await list("nowhere", fetcher(() => new Response("", { status: 404 })))).toBeNull();
  });

  test("anything else is", async () => {
    await expect(list("x", fetcher(() => new Response("", { status: 401 })))).rejects.toMatchObject({ status: 401 });
  });
});

test("href encodes each segment and nothing between them", () => {
  expect(href("desktop_backup/a b/#1?.jpg")).toBe("/files/desktop_backup/a%20b/%231%3F.jpg");
});

describe("ensure", () => {
  test("makes the folders above that are missing", async () => {
    const made = new Set(["/files/a/"]);
    const f = fetcher(({ url }) => {
      const parent = url.replace(/[^/]+\/$/, "");
      if (made.has(url)) return new Response("", { status: 405 });
      if (!made.has(parent) && parent !== "/files/") return new Response("", { status: 409 });
      made.add(url);
      return new Response("", { status: 201 });
    });

    await ensure("a/b/c", f);

    expect(made).toEqual(new Set(["/files/a/", "/files/a/b/", "/files/a/b/c/"]));
  });
});

describe("put", () => {
  test("never replaces: If-None-Match, and 412 is a conflict", async () => {
    const f = fetcher(() => new Response("", { status: 412 }));

    expect(await put("a/b.jpg", new Blob(["x"]), f)).toBe("conflict");
    expect(f.calls[0]).toMatchObject({ method: "PUT", url: "/files/a/b.jpg" });
    expect(f.calls[0].headers["If-None-Match"]).toBe("*");
  });

  test("201 is sent", async () => {
    expect(await put("a.jpg", new Blob(["x"]), fetcher(() => new Response(null, { status: 201 })))).toBe("sent");
  });
});

describe("tus", () => {
  // A tus server holding one upload in memory.
  function tusServer() {
    const s = { received: 0, length: 0, created: 0 };
    s.fetch = fetcher(({ method, headers, body }) => {
      if (method === "POST") {
        s.created++;
        s.length = Number(headers["Upload-Length"]);
        s.metadata = headers["Upload-Metadata"];
        s.received = 0;
        return new Response(null, { status: 201, headers: { Location: "/tus/abc" } });
      }
      if (method === "HEAD") {
        return new Response(null, { status: 200, headers: { "Upload-Offset": String(s.received) } });
      }
      if (method === "PATCH") {
        if (Number(headers["Upload-Offset"]) !== s.received) return new Response("", { status: 409 });
        s.received += body.size;
        return new Response(null, { status: 204, headers: { "Upload-Offset": String(s.received) } });
      }
    });
    return s;
  }

  const big = (size) => Object.assign(new Blob([new Uint8Array(size)]), { lastModified: 5 });

  test("creates with the path from the origin and sends it in chunks", async () => {
    const s = tusServer();
    const uploads = memory();

    expect(await tus("desktop_backup/vídeo.mp4", big(20 << 20), "7/vídeo.mp4", uploads, undefined, s.fetch)).toBe("sent");

    expect(atob(s.metadata.split(" ")[1])).toBe(
      String.fromCharCode(...new TextEncoder().encode("files/desktop_backup/vídeo.mp4")),
    );
    expect(s.received).toBe(20 << 20);
    expect(s.fetch.calls.filter((c) => c.method === "PATCH")).toHaveLength(3);
    expect(uploads.rows.size).toBe(0);
  });

  test("resumes from where the server says it stopped", async () => {
    const s = tusServer();
    s.received = 8 << 20;
    const uploads = memory();
    await uploads.put({ key: "7/v.mp4", url: "/tus/abc", size: 20 << 20, mtime: 5 });

    await tus("v.mp4", big(20 << 20), "7/v.mp4", uploads, undefined, s.fetch);

    expect(s.created).toBe(0);
    expect(s.fetch.calls.filter((c) => c.method === "PATCH")).toHaveLength(2);
  });

  test("starts again when the file changed since the upload began", async () => {
    const s = tusServer();
    const uploads = memory();
    await uploads.put({ key: "7/v.mp4", url: "/tus/old", size: 1, mtime: 1 });

    await tus("v.mp4", big(10 << 20), "7/v.mp4", uploads, undefined, s.fetch);

    expect(s.created).toBe(1);
    expect(s.fetch.calls.some((c) => c.url === "/tus/old")).toBe(false);
  });
});
