import { describe, expect, test, vi } from "vitest";
import { backUp, retry } from "../internal/web/backup/run.js";
import { HTTPError } from "../internal/web/backup/remote.js";
import { dir, file, memory } from "./fakes.js";

const noWait = { sleep: async () => {} };

// server is an in-memory tree under one destination, with every call counted.
function server(tree = {}) {
  const s = {
    tree,
    made: new Set(Object.keys(tree)),
    list: vi.fn(async (path) => (s.made.has(path) ? new Map(Object.entries(s.tree[path] || {})) : null)),
    ensure: vi.fn(async (path) => void s.made.add(path)),
    send: vi.fn(async (path, f) => {
      const at = path.split("/");
      const name = at.pop();
      const parent = at.join("/");
      s.tree[parent] = { ...s.tree[parent], [name]: { dir: false, size: f.size } };
      return "sent";
    }),
  };
  return s;
}

const folder = (handle) => ({ id: 7, name: "Pictures", dest: "desktop_backup/Pictures", handle });

describe("backUp", () => {
  test("sends what the server does not have, folders made on the way", async () => {
    const srv = server();
    const settled = memory();
    const handle = dir({ "a.jpg": file(3), trip: dir({ "b.jpg": file(5) }) });

    const tally = await backUp(folder(handle), { server: srv, settled, retryOpts: noWait });

    expect(tally).toMatchObject({ seen: 2, sent: 2, here: 0, conflicts: [], failed: [] });
    expect(srv.ensure.mock.calls.map((c) => c[0])).toEqual(["desktop_backup/Pictures", "desktop_backup/Pictures/trip"]);
    expect(srv.send.mock.calls.map((c) => c[0])).toEqual(["desktop_backup/Pictures/a.jpg", "desktop_backup/Pictures/trip/b.jpg"]);
    expect([...settled.rows.keys()]).toEqual(["7/a.jpg", "7/trip/b.jpg"]);
  });

  test("a second pass asks the server nothing", async () => {
    const srv = server();
    const settled = memory();
    const handle = dir({ "a.jpg": file(3) });
    await backUp(folder(handle), { server: srv, settled, retryOpts: noWait });
    srv.list.mockClear();
    srv.send.mockClear();

    const tally = await backUp(folder(handle), { server: srv, settled, retryOpts: noWait });

    expect(tally).toMatchObject({ seen: 1, sent: 0, here: 1 });
    expect(srv.list).not.toHaveBeenCalled();
    expect(srv.send).not.toHaveBeenCalled();
  });

  // stratus-app#124's third rule: settled only grows, so a deletion sticks.
  test("a file deleted on the server is not sent again", async () => {
    const srv = server();
    const settled = memory();
    const handle = dir({ "a.jpg": file(3) });
    await backUp(folder(handle), { server: srv, settled, retryOpts: noWait });
    srv.tree["desktop_backup/Pictures"] = {};
    srv.send.mockClear();

    await backUp(folder(handle), { server: srv, settled, retryOpts: noWait });

    expect(srv.send).not.toHaveBeenCalled();
  });

  test("a file changed here since it was settled is looked at again", async () => {
    const srv = server();
    const settled = memory();
    await backUp(folder(dir({ "a.jpg": file(3, 1) })), { server: srv, settled, retryOpts: noWait });

    const tally = await backUp(folder(dir({ "a.jpg": file(4, 2) })), { server: srv, settled, retryOpts: noWait });

    expect(tally.conflicts).toEqual(["a.jpg"]);
    expect(srv.send).toHaveBeenCalledTimes(1);
  });

  test("what the server already has at the same size is settled, which rebuilds a lost record", async () => {
    const srv = server({ "desktop_backup/Pictures": { "a.jpg": { dir: false, size: 3 } } });
    const settled = memory();

    const tally = await backUp(folder(dir({ "a.jpg": file(3) })), { server: srv, settled, retryOpts: noWait });

    expect(tally).toMatchObject({ sent: 0, here: 1 });
    expect(srv.ensure).not.toHaveBeenCalled();
    expect(settled.rows.has("7/a.jpg")).toBe(true);
  });

  test("another file at the same name is a conflict, left alone and not settled", async () => {
    const srv = server({ "desktop_backup/Pictures": { "a.jpg": { dir: false, size: 9 }, b: { dir: true, size: 0 } } });
    const settled = memory();

    const tally = await backUp(folder(dir({ "a.jpg": file(3), b: file(1) })), {
      server: srv,
      settled,
      retryOpts: noWait,
    });

    expect(tally.conflicts).toEqual(["a.jpg", "b"]);
    expect(srv.send).not.toHaveBeenCalled();
    expect(settled.rows.size).toBe(0);
  });

  test("a name taken between the listing and the send is a conflict", async () => {
    const srv = server();
    srv.send.mockResolvedValue("conflict");
    const settled = memory();

    const tally = await backUp(folder(dir({ "a.jpg": file(3) })), { server: srv, settled, retryOpts: noWait });

    expect(tally).toMatchObject({ sent: 0, conflicts: ["a.jpg"] });
    expect(settled.rows.size).toBe(0);
  });

  test("a file that fails is reported and the pass goes on", async () => {
    const srv = server();
    srv.send.mockRejectedValueOnce(new HTTPError(400, "sending a.jpg"));
    const settled = memory();

    const tally = await backUp(folder(dir({ "a.jpg": file(3), "b.jpg": file(3) })), {
      server: srv,
      settled,
      retryOpts: noWait,
    });

    expect(tally.failed).toEqual([{ path: "a.jpg", error: "sending a.jpg: 400" }]);
    expect(tally.sent).toBe(1);
  });

  test("a session that has gone stops the pass", async () => {
    const srv = server();
    srv.list.mockRejectedValue(new HTTPError(401, "listing"));

    await expect(
      backUp(folder(dir({ "a.jpg": file(3) })), { server: srv, settled: memory(), retryOpts: noWait }),
    ).rejects.toMatchObject({ status: 401 });
  });

  test("a network that drops is tried again", async () => {
    const srv = server();
    srv.send.mockRejectedValueOnce(new TypeError("Failed to fetch"));

    const tally = await backUp(folder(dir({ "a.jpg": file(3) })), {
      server: srv,
      settled: memory(),
      retryOpts: noWait,
    });

    expect(tally.sent).toBe(1);
    expect(srv.send).toHaveBeenCalledTimes(2);
  });
});

describe("retry", () => {
  test("gives up after the attempts it was given", async () => {
    const fn = vi.fn().mockRejectedValue(new HTTPError(503, "busy"));
    await expect(retry(fn, { attempts: 3, sleep: async () => {} })).rejects.toMatchObject({ status: 503 });
    expect(fn).toHaveBeenCalledTimes(3);
  });

  test("does not retry what another try will not change", async () => {
    const fn = vi.fn().mockRejectedValue(new HTTPError(400, "bad"));
    await expect(retry(fn, { sleep: async () => {} })).rejects.toMatchObject({ status: 400 });
    expect(fn).toHaveBeenCalledTimes(1);
  });
});
