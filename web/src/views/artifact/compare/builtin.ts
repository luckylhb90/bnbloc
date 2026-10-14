import { TEXTUAL_MATCH } from "../../../lib/artifactFiles.ts";
import { registerComparer } from "./registry.ts";

// The built-in comparers (docs/prd/2026-10-14-file-prompts-and-compare.md
// rule 6), registered on import like the built-in viewers. Each is data
// (what it claims, its rank) plus a strategy loaded on first use. PDF and
// Office formats come from sandboxed extraction (compare/remote.ts): their
// bytes are never parsed in the app's origin.

// A table compared cell by cell is bounded like a previewer's units.
const MAX_TABLE_CELLS = 5000;

registerComparer({
  id: "core.compare.text",
  label: "Text",
  match: TEXTUAL_MATCH,
  rank: 20,
  source: "builtin",
  load: async () => ({
    kind: "units",
    noun: ["line", "lines"],
    extract: async ({ blob }) => ({ units: [{ label: "", text: await blob.text() }] }),
  }),
});

registerComparer({
  id: "core.compare.table",
  label: "Table cells",
  match: { mimeTypes: ["text/csv", "text/tab-separated-values"], extensions: ["csv", "tsv"] },
  rank: 10,
  source: "builtin",
  load: async () => {
    const { tableUnits } = await import("./tableUnits.ts");
    return {
      kind: "units",
      noun: ["cell", "cells"],
      extract: async ({ blob, file }) => tableUnits(await blob.text(), { mimeType: file.mimeType, extension: file.extension }, MAX_TABLE_CELLS),
    };
  },
});

registerComparer({
  id: "core.compare.image",
  label: "Side by side",
  match: { mimeTypes: ["image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "image/svg+xml"], extensions: ["png", "jpg", "jpeg", "gif", "webp", "avif", "bmp", "svg"] },
  rank: 10,
  source: "builtin",
  load: async () => ({ kind: "view", View: (await import("./ImageCompare.tsx")).ImageCompare }),
});
