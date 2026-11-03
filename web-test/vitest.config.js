import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "happy-dom",
    // An origin for relative hrefs to resolve against, as a page has.
    environmentOptions: { happyDOM: { url: "http://stratus.test/backup/" } },
    include: ["*.test.js"],
  },
  // The modules under test are in the Go tree, outside this directory.
  server: { fs: { allow: [".."] } },
});
