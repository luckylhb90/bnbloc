#!/usr/bin/env node
// Builds the draw.io previewer into an installable bundle (layout and
// install: ../lib/bundle.mjs; `pnpm --dir examples/previewers/drawio install
// --frozen-lockfile` first).
//
// Usage: node examples/previewers/drawio/build.mjs

import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { buildBundle } from "../lib/bundle.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));

export function build({ outDir = join(HERE, "dist"), quiet = false } = {}) {
  return buildBundle({
    dir: HERE,
    title: "draw.io previewer",
    assets: ["index.html", "style.css"],
    outDir,
    quiet,
    // maxGraph carries an XML-over-HTTP loader this viewer never calls (the
    // frame has no network: connect-src 'none'); made unreachable, so the
    // bundle check for network calls stays strict.
    esbuild: { define: { "process.env.NODE_ENV": '"production"', XMLHttpRequest: "undefined", "window.XMLHttpRequest": "undefined" } },
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  build().catch((err) => {
    console.error(err instanceof Error ? err.message : err);
    process.exit(1);
  });
}
