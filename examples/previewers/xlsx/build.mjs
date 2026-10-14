#!/usr/bin/env node
// Builds the Excel previewer into an installable bundle (layout and
// install: ../lib/bundle.mjs; `pnpm --dir examples/previewers/xlsx install
// --frozen-lockfile` first).
//
// Usage: node examples/previewers/xlsx/build.mjs

import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { buildBundle } from "../lib/bundle.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));

export function build({ outDir = join(HERE, "dist"), quiet = false } = {}) {
  return buildBundle({
    dir: HERE,
    title: "Excel previewer",
    // ExcelJS's browser build (dist/exceljs.min.js) inlines its dependencies.
    prebuilt: ["exceljs"],
    assets: ["index.html", "style.css"],
    outDir,
    quiet,
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  build().catch((err) => {
    console.error(err instanceof Error ? err.message : err);
    process.exit(1);
  });
}
