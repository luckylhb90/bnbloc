// node --test examples/previewers/lib/bundle.test.mjs

import assert from "node:assert/strict";
import { test } from "node:test";

import { packTar } from "./bundle.mjs";
import { readTar } from "./bundle-check.mjs";

test("packTar sorts, pads files and checksums headers", () => {
  const tar = packTar({ "b.txt": Buffer.from("bb"), "a.txt": Buffer.alloc(513, 1) });
  const files = readTar(tar);
  assert.deepEqual([...files.keys()], ["a.txt", "b.txt"]);
  assert.equal(files.get("a.txt").length, 513);
  assert.equal(tar.length, 512 + 1024 + 512 + 512 + 1024);
});
