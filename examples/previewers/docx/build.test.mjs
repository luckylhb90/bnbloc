// node --test examples/previewers/docx/build.test.mjs
// (after `pnpm --dir examples/previewers/docx install --frozen-lockfile`)
// Builds the Word previewer into a temp dir and checks the bundle the way
// the gateway and desktop will read it (../lib/bundle-check.mjs), plus the
// bullet mapping.

import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { build } from "./build.mjs";
import { assertBundle } from "../lib/bundle-check.mjs";
import { unicodeBullets } from "./src/bullets.ts";

test("the build produces a deterministic, parseable bundle with its notices", async () => {
  const outA = await mkdtemp(join(tmpdir(), "docx-a-"));
  const outB = await mkdtemp(join(tmpdir(), "docx-b-"));
  try {
    const { files, pkg } = await assertBundle(await build({ outDir: outA, quiet: true }), await build({ outDir: outB, quiet: true }), {
      files: ["NOTICES.txt", "index.html", "main.js", "style.css"],
      toolbar: ["zoom", "fit"],
    });
    assert.deepEqual(pkg.previewer.match.extensions, [".docx", ".docm", ".dotx"], "OOXML documents only, never legacy .doc");
    assert.match(files.get("NOTICES.txt").toString("utf8"), /^docx-preview@0\.4\.1 — LICENSE$/m);
  } finally {
    await rm(outA, { recursive: true, force: true });
    await rm(outB, { recursive: true, force: true });
  }
});

test("Symbol and Wingdings bullets become Unicode; other text stays", () => {
  assert.equal(unicodeBullets('.n-0:before { content: "\t"; font-family: Symbol }'), '.n-0:before { content: "•\t"; font-family: Symbol }');
  assert.equal(unicodeBullets('"" "" "" ""'), '"▪" "➢" "❖" "✓"');
  assert.equal(unicodeBullets('content: "counter(a, decimal)."'), 'content: "counter(a, decimal)."');
  assert.equal(unicodeBullets('"o"'), '"o"', "Courier New's o is a real letter");
});

test("units: paragraphs and tables under their headings; an inserted paragraph is one addition", async () => {
  const { readFile } = await import("node:fs/promises");
  const JSZip = (await import("jszip")).default;
  const { docUnits } = await import("./src/units.ts");
  const { diffUnits } = await import("../../../web/src/lib/compareUnits.ts");
  const bytes = await readFile(new URL("./samples/doc.docx", import.meta.url));
  const before = await docUnits(JSZip, bytes);
  assert.ok(before.some((u) => u.label === "Table · By region" && u.text.startsWith("Region\tQ1\tQ2\nNorth\t120\t140")), "a table as tab-separated rows");
  const zip = await JSZip.loadAsync(bytes);
  const doc = await zip.file("word/document.xml").async("string");
  const anchor = doc.indexOf("<w:p", doc.indexOf("Support backlog halved") - 400);
  const inserted = '<w:p><w:r><w:t>A new point</w:t></w:r></w:p>';
  zip.file("word/document.xml", doc.slice(0, anchor) + inserted + doc.slice(anchor));
  const after = await docUnits(JSZip, await zip.generateAsync({ type: "uint8array" }));
  const changes = diffUnits(before, after).filter((c) => c.kind !== "same");
  assert.deepEqual(changes.map((c) => [c.kind, c.curr?.text]), [["added", "A new point"]]);
});
