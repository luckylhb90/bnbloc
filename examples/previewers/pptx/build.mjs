#!/usr/bin/env node
// Builds the PowerPoint previewer into an installable bundle (layout and
// install: ../lib/bundle.mjs). Besides index.html, main.js and style.css the
// bundle carries NOTICES.txt: the licenses of everything main.js bundles,
// read from the installed packages (`pnpm --dir examples/previewers/pptx
// install --frozen-lockfile` first).
//
// Usage: node examples/previewers/pptx/build.mjs

import { existsSync, readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { buildBundle } from "../lib/bundle.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const RENDERER = "@aiden0z/pptx-renderer";

/** Every package main.js bundles and the license files it ships. The
 *  renderer's own notices cover what its build inlines (mtx-decompressor,
 *  the ECMA-376 preset geometry). */
const NOTICES = [
  [RENDERER, ["LICENSE", "THIRD_PARTY_NOTICES.md", "licenses/mtx-decompressor-MPL-2.0.txt", "licenses/ECMA-text-copyright-notice.txt"]],
  ["echarts", ["LICENSE", "NOTICE"]],
  ["zrender", ["LICENSE"]],
  ["tslib", ["LICENSE.txt"]],
  ["jszip", ["LICENSE.markdown"]],
  ["pako", ["LICENSE"]],
];

/** The root of an installed package as esbuild sees it: resolved from here,
 *  else from the renderer, else from jszip (dependencies of dependencies),
 *  walking up from its entry to the package.json that names it (not every
 *  package exports ./package.json). */
function packageDir(name) {
  const req = createRequire(join(HERE, "package.json"));
  const bases = [req, createRequire(req.resolve(RENDERER))];
  bases.push(createRequire(bases[1].resolve("jszip")));
  for (const base of bases) {
    let entry;
    try {
      entry = base.resolve(name);
    } catch {
      continue;
    }
    for (let dir = dirname(entry); dir !== dirname(dir); dir = dirname(dir)) {
      if (existsSync(join(dir, "package.json")) && JSON.parse(readFileSync(join(dir, "package.json"), "utf8")).name === name) return dir;
    }
  }
  throw new Error(`${name} is not installed: pnpm --dir examples/previewers/pptx install --frozen-lockfile`);
}

export async function notices() {
  const parts = ["Third-party software bundled in main.js of the bnbloc PowerPoint previewer.\n"];
  for (const [name, files] of NOTICES) {
    const dir = packageDir(name);
    const { version } = JSON.parse(await readFile(join(dir, "package.json"), "utf8"));
    for (const file of files) {
      const text = await readFile(join(dir, file), "utf8");
      parts.push(`${"=".repeat(78)}\n${name}@${version} — ${file}\n${"=".repeat(78)}\n\n${text.trimEnd()}\n`);
    }
  }
  return parts.join("\n");
}

export async function build({ outDir = join(HERE, "dist"), quiet = false } = {}) {
  return buildBundle({
    dir: HERE,
    assets: ["index.html", "style.css"],
    generated: { "NOTICES.txt": Buffer.from(await notices()) },
    outDir,
    quiet,
    // The renderer's ESM build reads process.env.NODE_ENV; PDF.js (EMF
    // fallback previews) is disabled, so its dynamic import never runs.
    esbuild: { define: { "process.env.NODE_ENV": '"production"' } },
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  build().catch((err) => {
    console.error(err instanceof Error ? err.message : err);
    process.exit(1);
  });
}
