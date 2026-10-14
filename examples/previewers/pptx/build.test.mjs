// node --test examples/previewers/pptx/build.test.mjs
// (after `pnpm --dir examples/previewers/pptx install --frozen-lockfile`)
// Builds the PowerPoint previewer into a temp dir and checks the bundle the
// way the gateway and desktop will read it (../lib/bundle-check.mjs), its
// notices, and the deck helpers.

import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { build } from "./build.mjs";
import { assertBundle } from "../lib/bundle-check.mjs";
import { deckTitle, sniffDeck, unreadableMessage } from "./src/deck.ts";

const HERE = dirname(fileURLToPath(import.meta.url));

test("the build produces a deterministic, parseable bundle with its notices", async () => {
  const outA = await mkdtemp(join(tmpdir(), "pptx-a-"));
  const outB = await mkdtemp(join(tmpdir(), "pptx-b-"));
  try {
    const { files, pkg } = await assertBundle(await build({ outDir: outA, quiet: true }), await build({ outDir: outB, quiet: true }), {
      files: ["NOTICES.txt", "index.html", "main.js", "style.css"],
      toolbar: ["zoom", "fit"],
    });
    assert.deepEqual(pkg.previewer.match.extensions, [".pptx", ".pptm", ".ppsx", ".potx"], "OOXML decks only, never legacy .ppt");
    const notices = files.get("NOTICES.txt").toString("utf8");
    for (const name of ["@aiden0z/pptx-renderer", "echarts", "zrender", "tslib", "jszip", "pako"]) {
      assert.match(notices, new RegExp(`^${name.replace(/[/@]/g, "\\$&")}@\\d+\\.\\d+\\.\\d+ — `, "m"), `${name} notice`);
    }
    assert.match(notices, /Mozilla Public License/, "the MPL-2.0 text of the bundled mtx-decompressor");
    const js = files.get("main.js").toString("utf8");
    assert.doesNotMatch(js, /\beval\(/, "no eval (the CSP refuses it)");
    assert.ok(files.get("main.js").length < 4 << 20, "main.js stays well under the 16 MiB bundle cap");
  } finally {
    await rm(outA, { recursive: true, force: true });
    await rm(outB, { recursive: true, force: true });
  }
});

test("sniffDeck tells a deck from an encrypted/legacy file and from anything else", async () => {
  const deck = await readFile(join(HERE, "samples", "deck.pptx"));
  assert.equal(sniffDeck(deck.buffer.slice(deck.byteOffset, deck.byteOffset + deck.byteLength)), "zip");
  assert.equal(sniffDeck(new Uint8Array([0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1, 0]).buffer), "ole");
  assert.equal(sniffDeck(new TextEncoder().encode("hello").buffer), "unknown");
  assert.equal(sniffDeck(new ArrayBuffer(0)), "unknown");
  assert.match(unreadableMessage("ole", "q3.pptx"), /^q3\.pptx is password-protected or in the old \.ppt format/);
  assert.match(unreadableMessage("unknown", "x.pptx"), /is not a PowerPoint presentation/);
});

test("deckTitle names the slide in view, 1-based and clamped", () => {
  assert.equal(deckTitle("q3.pptx", 0, 12), "q3.pptx — slide 1 of 12");
  assert.equal(deckTitle("q3.pptx", 11, 12), "q3.pptx — slide 12 of 12");
  assert.equal(deckTitle("q3.pptx", 40, 12), "q3.pptx — slide 12 of 12");
  assert.equal(deckTitle("q3.pptx", 0, 0), "q3.pptx");
  assert.equal([...deckTitle("x".repeat(200) + ".pptx", 0, 1)].length, 80 + " — slide 1 of 1".length);
});
