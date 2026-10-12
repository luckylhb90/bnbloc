#!/usr/bin/env node
// File-length gate for web/. Business-code files stay at or below MAX_LINES
// counted lines, where blank lines and comment-only lines are excluded — the
// same "meaningful lines" rule the Go side of the product applies. Keeping the
// web client's files bounded forces decomposition before a view accretes into
// an unreviewable monolith (this repo has hit that with RoomView/Composer).
//
// Usage: node scripts/check-file-length.mjs
// Exits non-zero and lists every offender when any file is over the limit.

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, extname } from "node:path";
import { fileURLToPath } from "node:url";

const MAX_LINES = 800;
const ROOT = join(fileURLToPath(new URL(".", import.meta.url)), "..", "src");
const EXTS = new Set([".ts", ".tsx"]);

// Count lines that are neither blank nor comment-only. Tracks block comments
// (/* ... */) across lines so a long banner comment does not inflate the count.
function meaningfulLineCount(source) {
  let count = 0;
  let inBlock = false;
  for (const raw of source.split("\n")) {
    const line = raw.trim();
    if (inBlock) {
      const end = line.indexOf("*/");
      if (end === -1) continue;
      const after = line.slice(end + 2).trim();
      inBlock = false;
      if (after === "" || after.startsWith("//")) continue;
      count++;
      continue;
    }
    if (line === "") continue;
    if (line.startsWith("//")) continue;
    if (line.startsWith("/*")) {
      if (line.indexOf("*/", 2) === -1) inBlock = true;
      const rest = inBlock ? "" : line.slice(line.indexOf("*/", 2) + 2).trim();
      if (rest === "" || rest.startsWith("//")) continue;
      count++;
      continue;
    }
    count++;
  }
  return count;
}

function walk(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const s = statSync(p);
    if (s.isDirectory()) out.push(...walk(p));
    // A generated file (*.gen.ts) is reviewed at its Go source.
    else if (EXTS.has(extname(name)) && !name.endsWith(".gen.ts")) out.push(p);
  }
  return out;
}

const offenders = [];
for (const file of walk(ROOT)) {
  const n = meaningfulLineCount(readFileSync(file, "utf8"));
  if (n > MAX_LINES) offenders.push({ file, n });
}

if (offenders.length > 0) {
  offenders.sort((a, b) => b.n - a.n);
  console.error(`File-length check failed: ${offenders.length} file(s) over ${MAX_LINES} meaningful lines.`);
  for (const { file, n } of offenders) {
    console.error(`  ${n}\t${file.slice(file.indexOf("/src/") + 1)}`);
  }
  console.error("Decompose these before adding more logic (blank + comment lines are already excluded).");
  process.exit(1);
}

console.log(`File-length check passed: all files <= ${MAX_LINES} meaningful lines.`);
