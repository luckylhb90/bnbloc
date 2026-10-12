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

// The type names internal/wire/cmd/wiregen generates (lib/wire.gen.ts).
const WIRE_TYPES = [...readFileSync(join(SRC, "lib", "wire.gen.ts"), "utf8").matchAll(/^export (?:interface|type) (\w+)/gm)].map((m) => m[1]);

export const RULES = [
  {
    id: "T8",
    name: "a server shape is generated from internal/wire (wire.gen.ts), never redeclared",
    // A hand-written mirror drifts from Go (web M26-M28): re-export the
    // generated type (export type { X } from "./wire.gen.ts") instead.
    pattern: new RegExp(`^export (?:interface|type) (?:${WIRE_TYPES.join("|")})\\b`, "m"),
    allowed: ["lib/wire.gen.ts"],
    sample: { file: "lib/api.ts", text: "export interface AgentJob {\n  jobId: string;\n}" },
  },
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
  {
    id: 10,
    name: "clipboard writes go through lib/clipboard.ts (ui/useCopy for the toast)",
    // navigator.clipboard is missing on an http gateway; hand-rolled copies
    // reported "copied" when nothing was (web M23).
    pattern: /navigator\.clipboard/,
    allowed: ["lib/clipboard.ts"],
    sample: { file: "views/X.tsx", text: "void navigator.clipboard?.writeText(url);" },
  },
  {
    id: "L8",
    name: "click-outside dismissal goes through ui/useDismissOnOutside",
    pattern: /addEventListener\("mousedown"/,
    allowed: ["ui/useDismissOnOutside.ts"],
    sample: { file: "views/X.tsx", text: 'document.addEventListener("mousedown", onDoc);' },
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
