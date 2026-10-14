// node --test examples/previewers/lib/office.test.mjs

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { countedTitle, sniffOffice, unreadableMessage } from "./office.ts";

const HERE = dirname(fileURLToPath(import.meta.url));
const WORD = { noun: "Word document", ext: ".docx", legacy: ".doc", apps: "Word or Pages" };

test("sniffOffice tells a package from an encrypted/legacy file and from anything else", async () => {
  const deck = await readFile(join(HERE, "..", "pptx", "samples", "deck.pptx"));
  assert.equal(sniffOffice(deck.buffer.slice(deck.byteOffset, deck.byteOffset + deck.byteLength)), "zip");
  assert.equal(sniffOffice(new Uint8Array([0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1, 0]).buffer), "ole");
  assert.equal(sniffOffice(new TextEncoder().encode("hello").buffer), "unknown");
  assert.equal(sniffOffice(new ArrayBuffer(0)), "unknown");
});

test("unreadableMessage names the file, the family and what to do", () => {
  assert.equal(unreadableMessage(WORD, "ole", "q3.docx"), "q3.docx is password-protected or in the old .doc format; download it to open it in Word or Pages.");
  assert.equal(unreadableMessage(WORD, "unknown", "x.docx"), "x.docx is not a Word document (.docx); download it to open it.");
  assert.equal(unreadableMessage(WORD, "zip", "x.docx"), "x.docx could not be read as a Word document; download it to open it.");
});

test("countedTitle names the unit in view, 1-based and clamped", () => {
  assert.equal(countedTitle("q3.pptx", "slide", 0, 12), "q3.pptx — slide 1 of 12");
  assert.equal(countedTitle("q3.pptx", "slide", 11, 12), "q3.pptx — slide 12 of 12");
  assert.equal(countedTitle("q3.pptx", "slide", 40, 12), "q3.pptx — slide 12 of 12");
  assert.equal(countedTitle("q3.pptx", "slide", 0, 0), "q3.pptx");
  assert.equal([...countedTitle("x".repeat(200) + ".pptx", "sheet", 0, 1)].length, 80 + " — sheet 1 of 1".length);
});
