import { artifactRunnerUrl, type ConvertFileSrc } from "@/lib/artifactFrame";

// What a PDF guest frame needs (pdfFrame.ts), for the viewer and for
// Compare alike: the sandboxed runner URL, and pdf.js (library + worker,
// ~1.8 MB raw / ~0.5 MB gzip) as source text, a lazy chunk fetched once.

export function pdfRunnerUrl(): string {
  const internals = window.__TAURI_INTERNALS__ as { convertFileSrc?: ConvertFileSrc } | undefined;
  return artifactRunnerUrl({ convertFileSrc: internals?.convertFileSrc, baseURI: document.baseURI, hostOrigin: window.location.origin });
}

let pdfjsSources: Promise<{ lib: string; worker: string }> | null = null;

export function loadPdfjs(): Promise<{ lib: string; worker: string }> {
  pdfjsSources ??= Promise.all([
    import("pdfjs-dist/legacy/build/pdf.min.mjs?raw"),
    import("pdfjs-dist/legacy/build/pdf.worker.min.mjs?raw"),
  ]).then(([lib, worker]) => ({ lib: lib.default, worker: worker.default }));
  pdfjsSources.catch(() => {
    pdfjsSources = null;
  });
  return pdfjsSources;
}
