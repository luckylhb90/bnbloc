#!/usr/bin/env node
// Web boundary gate: a rule that has one owner in web/src may only be
// written in that owner's file (AGENTS.md "One rule, one owner"). Each rule
// is a pattern plus the files allowed to contain it; tests (*.test.ts) are
// exempt. check-boundaries.test.mjs runs the gate over web/src (so
// `scripts/ci.sh web` enforces it) and checks every rule catches its sample.
//
// Usage: node scripts/check-boundaries.mjs   (exits non-zero on a violation)

import { readFileSync, readdirSync, statSync } from "node:fs";
import { extname, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

export const SRC = join(fileURLToPath(new URL(".", import.meta.url)), "..", "src");

export const RULES = [
  {
    id: 6,
    name: "the session token rides a URL only on the room event stream",
    // EventSource cannot set headers, so sse.ts passes the session as
    // access_token; every other link (attachments, downloads) carries a
    // per-resource ticket instead (web H10).
    pattern: /access_token=/,
    allowed: ["lib/sse.ts"],
    sample: { file: "lib/api.ts", text: "return `${base}?access_token=${token}`;" },
  },
];

// violations returns "<file>: rule <id> (<name>)" for every source file (a
// path relative to src, "/"-separated) breaking a rule.
export function violations(files) {
  const out = [];
  for (const { file, text } of files) {
    if (/\.test\.tsx?$/.test(file)) continue;
    for (const rule of RULES) {
      if (rule.allowed.includes(file) || !rule.pattern.test(text)) continue;
      out.push(`${file}: rule ${rule.id} (${rule.name})`);
    }
  }
  return out;
}

export function sourceFiles(root = SRC) {
  const out = [];
  const walk = (dir) => {
    for (const name of readdirSync(dir)) {
      const path = join(dir, name);
      if (statSync(path).isDirectory()) walk(path);
      else if ([".ts", ".tsx"].includes(extname(name))) {
        out.push({ file: relative(root, path).split(sep).join("/"), text: readFileSync(path, "utf8") });
      }
    }
  };
  walk(root);
  return out;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const found = violations(sourceFiles());
  for (const v of found) console.error(v);
  process.exit(found.length > 0 ? 1 : 0);
}
