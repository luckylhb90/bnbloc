// node --test examples/previewers/sprite-sheet/build.test.mjs
// Builds the reference previewer into a temp dir and checks the bundle the way
// the gateway and desktop will read it: a strict ustar parse, the entry, the
// sha256, the package shape against the protocol vocabularies, plus the atlas
// parser on both common JSON shapes.

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { build, packTar } from "./build.mjs";
import { cellSize, isAtlasFile, parseAtlas } from "./src/atlas.ts";
import { PREVIEWER_CAPABILITIES, readToolbar } from "../../../web/previewer-sdk/src/protocol.ts";

const HERE = dirname(fileURLToPath(import.meta.url));

/** Strict ustar reader: checksum, magic, regular files only, zero end blocks. */
function readTar(buf) {
  const files = new Map();
  let off = 0;
  for (;;) {
    assert.ok(off + 512 <= buf.length, "archive truncated before its end blocks");
    const header = buf.subarray(off, off + 512);
    if (header.every((b) => b === 0)) {
      assert.ok(buf.subarray(off, off + 1024).every((b) => b === 0), "two zero end blocks");
      return files;
    }
    const field = (start, len) => header.subarray(start, start + len).toString("ascii").replace(/\0.*$/s, "").trim();
    let sum = 0;
    for (let i = 0; i < 512; i++) sum += i >= 148 && i < 156 ? 32 : header[i];
    assert.equal(parseInt(field(148, 8), 8), sum, "header checksum");
    assert.equal(header.subarray(257, 263).toString("ascii"), "ustar\0", "ustar magic");
    assert.equal(field(156, 1), "0", "regular file");
    assert.equal(field(136, 12), "00000000000", "mtime 0 (deterministic)");
    const name = field(0, 100);
    const size = parseInt(field(124, 12), 8);
    files.set(name, buf.subarray(off + 512, off + 512 + size));
    off += 512 + Math.ceil(size / 512) * 512;
  }
}

test("the build produces a deterministic, parseable bundle and its package", async () => {
  const outA = await mkdtemp(join(tmpdir(), "sprite-sheet-a-"));
  const outB = await mkdtemp(join(tmpdir(), "sprite-sheet-b-"));
  try {
    const a = await build({ outDir: outA, quiet: true });
    const b = await build({ outDir: outB, quiet: true });
    assert.equal(a.sha256, b.sha256, "two builds produce the same archive");

    const tar = await readFile(a.tarPath);
    assert.equal(createHash("sha256").update(tar).digest("hex"), a.sha256, "sha256 of the archive");
    assert.equal((await readFile(`${a.tarPath}.sha256`, "utf8")).trim(), a.sha256, "sha256 side file");
    const files = readTar(tar);
    assert.deepEqual([...files.keys()], ["index.html", "main.js", "style.css"], "sorted bundle files");

    const pkg = JSON.parse(await readFile(a.packagePath, "utf8"));
    assert.equal(pkg.kind, "bnbloc.previewer.package/v1");
    assert.equal(pkg.contract, "bnbloc.previewer.contract/v1");
    assert.equal(pkg.runtime, "bundle");
    assert.deepEqual(pkg.artifacts, { bundle: { url: "", sha256: "" } }, "upload placeholder kept for the publish CLI");
    assert.ok(files.has(pkg.previewer.entry), "entry is in the bundle");
    assert.ok(pkg.previewer.rank >= 1 && pkg.previewer.rank <= 1000, "rank bounds");
    for (const cap of pkg.previewer.capabilities) assert.ok(PREVIEWER_CAPABILITIES.includes(cap), `capability ${cap}`);
    const toolbar = readToolbar(pkg.previewer.toolbar);
    assert.ok(toolbar, "declared toolbar is valid under the protocol rules");
    assert.deepEqual(toolbar.map((t) => t.id), ["zoom", "fit", "grid", "background", "frames"]);
    for (const ext of pkg.previewer.match.extensions) assert.match(ext, /^\.[a-z0-9]+(\.[a-z0-9]+){0,2}$/);
    for (const mime of pkg.previewer.match.mimeTypes) assert.match(mime, /^[a-z0-9.+-]+\/([a-z0-9.+-]+|\*)$/);

    const html = files.get("index.html").toString("utf8");
    assert.match(html, /<script src="main\.js" defer><\/script>/, "external classic script");
    assert.doesNotMatch(html, /<script>/, "no inline script (the CSP refuses it)");
    assert.doesNotMatch(files.get("main.js").toString("utf8"), /\bfetch\(|XMLHttpRequest|WebSocket/, "no network calls");
  } finally {
    await rm(outA, { recursive: true, force: true });
    await rm(outB, { recursive: true, force: true });
  }
});

test("packTar pads files and checksums headers", () => {
  const tar = packTar({ "b.txt": Buffer.from("bb"), "a.txt": Buffer.alloc(513, 1) });
  const files = readTar(tar);
  assert.deepEqual([...files.keys()], ["a.txt", "b.txt"]);
  assert.equal(files.get("a.txt").length, 513);
  assert.equal(tar.length, 512 + 1024 + 512 + 512 + 1024);
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
