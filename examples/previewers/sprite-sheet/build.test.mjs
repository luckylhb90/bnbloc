// node --test examples/previewers/sprite-sheet/build.test.mjs
// Builds the reference previewer into a temp dir and checks the bundle the way
// the gateway and desktop will read it (../lib/bundle-check.mjs), plus the
// atlas parser on both common JSON shapes.

import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { build } from "./build.mjs";
import { assertBundle } from "../lib/bundle-check.mjs";
import { cellSize, isAtlasFile, parseAtlas } from "./src/atlas.ts";

const HERE = dirname(fileURLToPath(import.meta.url));

test("the build produces a deterministic, parseable bundle and its package", async () => {
  const outA = await mkdtemp(join(tmpdir(), "sprite-sheet-a-"));
  const outB = await mkdtemp(join(tmpdir(), "sprite-sheet-b-"));
  try {
    await assertBundle(await build({ outDir: outA, quiet: true }), await build({ outDir: outB, quiet: true }), {
      files: ["index.html", "main.js", "style.css"],
      toolbar: ["zoom", "fit", "grid", "background", "frames"],
    });
  } finally {
    await rm(outA, { recursive: true, force: true });
    await rm(outB, { recursive: true, force: true });
  }
});

test("atlas parsing: hash and array forms", async () => {
  const sample = parseAtlas(await readFile(join(HERE, "samples", "sheet.atlas.json"), "utf8"));
  assert.equal(sample.frames.length, 16);
  assert.deepEqual([sample.width, sample.height, sample.image], [64, 64, "sheet.png"]);
  const arr = parseAtlas(JSON.stringify({ frames: [{ filename: "a", frame: { x: 0, y: 0, w: 8, h: 8 } }, { frame: { x: 8, y: 0, w: 8, h: 4 } }] }));
  assert.deepEqual(arr.frames.map((f) => f.name), ["a", "frame 2"]);
  assert.deepEqual([arr.width, arr.height], [16, 8], "size from the bounding box without meta.size");
  assert.throws(() => parseAtlas("{"), /not valid JSON/);
  assert.throws(() => parseAtlas('{"textures": []}'), /not a sprite-sheet atlas/);
  assert.throws(() => parseAtlas('{"frames": {"x": {"frame": {"x": 0, "y": 0, "w": 0, "h": 1}}}}'), /no valid frames/);
  assert.ok(isAtlasFile("hero.atlas.json", "application/octet-stream"));
  assert.ok(!isAtlasFile("hero.png", "image/png"));
  assert.equal(cellSize(64, 64), 16);
  assert.equal(cellSize(100, 60), 32, "fallback when nothing divides");
});

test("the committed sample png is a small valid PNG", async () => {
  const png = await readFile(join(HERE, "samples", "sheet.png"));
  assert.ok(png.length <= 4096);
  assert.deepEqual([...png.subarray(0, 8)], [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  assert.equal(png.readUInt32BE(16), 64, "width");
  assert.equal(png.readUInt32BE(20), 64, "height");
});
