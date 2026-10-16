#!/usr/bin/env node
// Builds the Mermaid previewer into an installable bundle (layout and
// install: ../lib/bundle.mjs; `pnpm --dir examples/previewers/mermaid install
// --frozen-lockfile` first).
//
// Usage: node examples/previewers/mermaid/build.mjs

import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { buildBundle } from "../lib/bundle.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));

export function build({ outDir = join(HERE, "dist"), quiet = false } = {}) {
  return buildBundle({
    dir: HERE,
    title: "Mermaid previewer",
    assets: ["index.html", "style.css"],
    outDir,
    quiet,
    esbuild: { define: { "process.env.NODE_ENV": '"production"' } },
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  build().catch((err) => {
    console.error(err instanceof Error ? err.message : err);
    process.exit(1);
  });
}
