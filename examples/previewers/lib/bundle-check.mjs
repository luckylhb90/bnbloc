// Test helpers for previewer bundles: read the archive the way the gateway
// and desktop will (strict ustar), and check a built bundle and its package
// against the protocol vocabularies.

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { join } from "node:path";

import { PREVIEWER_CAPABILITIES, readToolbar } from "../../../web/previewer-sdk/src/protocol.ts";

/** Strict ustar reader: checksum, magic, regular files only, zero end blocks. */
export function readTar(buf) {
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

/** Checks two builds a and b of one previewer: same archive, the sha256 side
 *  file, the sorted file list, the package shape, the toolbar ids, the
 *  claimed extensions/MIME types, no inline script, no network calls.
 *  Returns the archive's files. */
export async function assertBundle(a, b, { files: want, toolbar: toolbarIds }) {
  assert.equal(a.sha256, b.sha256, "two builds produce the same archive");
  const tar = await readFile(a.tarPath);
  assert.equal(createHash("sha256").update(tar).digest("hex"), a.sha256, "sha256 of the archive");
  assert.equal((await readFile(`${a.tarPath}.sha256`, "utf8")).trim(), a.sha256, "sha256 side file");
  const files = readTar(tar);
  assert.deepEqual([...files.keys()], want, "sorted bundle files");

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
  assert.deepEqual(toolbar.map((t) => t.id), toolbarIds);
  for (const ext of pkg.previewer.match.extensions ?? []) assert.match(ext, /^\.[a-z0-9]+(\.[a-z0-9]+){0,2}$/);
  for (const mime of pkg.previewer.match.mimeTypes ?? []) assert.match(mime, /^[a-z0-9.+-]+\/([a-z0-9.+-]+|\*)$/);

  const html = files.get(pkg.previewer.entry).toString("utf8");
  assert.match(html, /<script src="main\.js" defer><\/script>/, "external classic script");
  assert.doesNotMatch(html, /<script>/, "no inline script (the CSP refuses it)");
  assert.doesNotMatch(files.get("main.js").toString("utf8"), /\bfetch\(|XMLHttpRequest|WebSocket\(/, "no network calls");
  // Every inlined third-party package has its licenses in NOTICES.txt.
  if (a.packages.length > 0) {
    assert.ok(files.has("NOTICES.txt"), "NOTICES.txt for the bundled packages");
    const notices = files.get("NOTICES.txt").toString("utf8");
    for (const root of a.packages) {
      const { name } = JSON.parse(await readFile(join(root, "package.json"), "utf8"));
      assert.ok(notices.includes(`\n${name}@`), `${name} has its notice`);
    }
  } else {
    assert.ok(!files.has("NOTICES.txt"), "no NOTICES.txt without third-party code");
  }
  return { files, pkg };
}
