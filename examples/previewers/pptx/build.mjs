#!/usr/bin/env node
// Builds the PowerPoint previewer into an installable bundle (layout and
// install: ../lib/bundle.mjs; `pnpm --dir examples/previewers/pptx install
// --frozen-lockfile` first).
//
// Usage: node examples/previewers/pptx/build.mjs

import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { buildBundle } from "../lib/bundle.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));

export function build({ outDir = join(HERE, "dist"), quiet = false } = {}) {
  return buildBundle({
    dir: HERE,
    title: "PowerPoint previewer",
    assets: ["index.html", "style.css"],
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
