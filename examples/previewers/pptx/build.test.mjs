// node --test examples/previewers/pptx/build.test.mjs
// (after `pnpm --dir examples/previewers/pptx install --frozen-lockfile`)
// Builds the PowerPoint previewer into a temp dir and checks the bundle the
// way the gateway and desktop will read it (../lib/bundle-check.mjs) and its
// notices.

import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { build } from "./build.mjs";
import { assertBundle } from "../lib/bundle-check.mjs";


test("the build produces a deterministic, parseable bundle with its notices", async () => {
  const outA = await mkdtemp(join(tmpdir(), "pptx-a-"));
  const outB = await mkdtemp(join(tmpdir(), "pptx-b-"));
  try {
    const { files, pkg } = await assertBundle(await build({ outDir: outA, quiet: true }), await build({ outDir: outB, quiet: true }), {
      files: ["NOTICES.txt", "index.html", "main.js", "style.css"],
      toolbar: ["zoom", "fit"],
    });
    assert.deepEqual(pkg.previewer.match.extensions, [".pptx", ".pptm", ".ppsx", ".potx"], "OOXML decks only, never legacy .ppt");
    assert.match(files.get("NOTICES.txt").toString("utf8"), /Mozilla Public License/, "the MPL-2.0 text of the bundled mtx-decompressor");
    const js = files.get("main.js").toString("utf8");
    assert.doesNotMatch(js, /\beval\(/, "no eval (the CSP refuses it)");
    assert.ok(files.get("main.js").length < 4 << 20, "main.js stays well under the 16 MiB bundle cap");
  } finally {
    await rm(outA, { recursive: true, force: true });
    await rm(outB, { recursive: true, force: true });
  }
});
