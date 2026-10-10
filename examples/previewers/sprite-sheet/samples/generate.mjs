#!/usr/bin/env node
// Regenerates the committed sample files deterministically:
//   sheet.png         64×64 RGBA sprite sheet, 4×4 cells of 16 px
//   sheet.atlas.json  the matching atlas (hash form)
// Usage: node examples/previewers/sprite-sheet/samples/generate.mjs

import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { crc32, deflateSync } from "node:zlib";

const HERE = dirname(fileURLToPath(import.meta.url));
const CELL = 16;
const COLS = 4;
const SIDE = CELL * COLS;

const COLORS = [
  [231, 76, 60],
  [230, 126, 34],
  [241, 196, 15],
  [46, 204, 113],
  [26, 188, 156],
  [52, 152, 219],
  [155, 89, 182],
  [236, 112, 160],
];

/** Pixel of sprite `index` at (x, y) inside its cell: a filled disc whose
 *  radius grows with the frame number, on transparency. */
function sprite(index, x, y) {
  const [r, g, b] = COLORS[index % COLORS.length];
  const radius = 3 + (index % 4) * 1.4;
  const dx = x - 7.5;
  const dy = y - 7.5;
  const d = Math.sqrt(dx * dx + dy * dy);
  if (d > radius) return [0, 0, 0, 0];
  const shade = d > radius - 1.2 ? 0.55 : 1;
  return [Math.round(r * shade), Math.round(g * shade), Math.round(b * shade), 255];
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}

function png() {
  const raw = Buffer.alloc((SIDE * 4 + 1) * SIDE);
  for (let y = 0; y < SIDE; y++) {
    const row = y * (SIDE * 4 + 1);
    raw[row] = 0; // filter: none
    for (let x = 0; x < SIDE; x++) {
      const index = Math.floor(y / CELL) * COLS + Math.floor(x / CELL);
      raw.set(sprite(index, x % CELL, y % CELL), row + 1 + x * 4);
    }
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(SIDE, 0);
  ihdr.writeUInt32BE(SIDE, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

function atlas() {
  const frames = {};
  for (let i = 0; i < COLS * COLS; i++) {
    frames[`orb_${String(i).padStart(2, "0")}`] = {
      frame: { x: (i % COLS) * CELL, y: Math.floor(i / COLS) * CELL, w: CELL, h: CELL },
    };
  }
  return { frames, meta: { image: "sheet.png", size: { w: SIDE, h: SIDE }, scale: "1" } };
}

writeFileSync(join(HERE, "sheet.png"), png());
writeFileSync(join(HERE, "sheet.atlas.json"), JSON.stringify(atlas(), null, 2) + "\n");
console.log(`wrote ${join(HERE, "sheet.png")} and ${join(HERE, "sheet.atlas.json")}`);
