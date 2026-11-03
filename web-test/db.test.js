import { IDBFactory } from "fake-indexeddb";
import "fake-indexeddb/auto";
import { expect, test } from "vitest";
import { open, store } from "../internal/web/backup/db.js";

test("forgetting a folder's files takes those and no other folder's", async () => {
  const db = await open(new IDBFactory());
  const settled = store(db, "settled");
  for (const key of ["1/a", "1/b/c", "12/a", "2/a"]) {
    await settled.put({ key, size: 1, mtime: 1 });
  }

  await settled.delPrefix("1/");

  expect((await settled.all()).map((r) => r.key)).toEqual(["12/a", "2/a"]);
});

test("folders are numbered by the store", async () => {
  const db = await open(new IDBFactory());
  const folders = store(db, "folders");

  const id = await folders.put({ name: "Pictures", dest: "desktop_backup/Pictures" });

  expect(await folders.get(id)).toMatchObject({ id, name: "Pictures" });
});
