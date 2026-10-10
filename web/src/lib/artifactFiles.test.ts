import {
  artifactExtension,
  artifactFileName,
  downloadFileName,
  fileCategory,
  fileExtension,
  fileTypeLabel,
  inferMime,
  isPreviewableImage,
  isTextual,
  SITE_MIME,
} from "./artifactFiles.ts";

function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

test("extensions come from the last path segment", () => {
  eq(fileExtension("src/app/Main.TSX"), "tsx", "path + case");
  eq(fileExtension(".env"), "", "dotfile");
  eq(fileExtension("archive.tar.gz"), "gz", "last dot");
  eq(fileExtension("README"), "", "none");
  eq(fileExtension("weird.<script>"), "", "rejects junk");
});

test("MIME: declared, else extension, else legacy kind", () => {
  eq(inferMime({ mimeType: "Image/PNG; x=1", fileName: "a.jpg" }), "image/png", "declared wins");
  eq(inferMime({ mimeType: "application/octet-stream", fileName: "a.pdf" }), "application/pdf", "octet-stream yields");
  eq(inferMime({ kind: "code", title: "web/src/App.tsx" }), "text/tsx", "title path");
  eq(inferMime({ kind: "code", language: "python", title: "script" }), "text/x-python", "language");
  eq(inferMime({ kind: "markdown", title: "Spec" }), "text/markdown", "kind");
  eq(inferMime({ kind: "html", title: "Page" }), "", "inline html stays source");
  eq(inferMime({ kind: "app", title: "Game" }), "text/html", "app runs");
  eq(inferMime({ kind: "file", fileName: "blob.bin" }), "", "unknown");
});

test("download names gain an extension from the kind", () => {
  eq(artifactFileName({ kind: "markdown", title: "Launch plan" }), "Launch plan.md", "markdown");
  eq(artifactFileName({ kind: "code", title: "util", language: "go" }), "util.go", "code language");
  eq(artifactFileName({ kind: "file", fileName: "out/report.pdf", title: "Report" }), "report.pdf", "file name, last segment");
  eq(artifactFileName({ kind: "image", mimeType: "image/png", title: "Logo" }), "Logo.png", "from MIME");
  eq(artifactExtension({ kind: "code", title: "x", language: "rust" }), "rs", "language ext");
});

test("categories, text-ness and labels", () => {
  eq(fileCategory({ fileName: "a.png" }), "image", "image");
  eq(fileCategory({ kind: "image", title: "legacy" }), "image", "legacy image");
  eq(fileCategory({ fileName: "a.pdf" }), "pdf", "pdf");
  eq(fileCategory({ fileName: "a.csv" }), "table", "csv");
  eq(fileCategory({ fileName: "a.json" }), "json", "json");
  eq(fileCategory({ kind: "app", title: "x" }), "web", "app");
  eq(fileCategory({ kind: "html", title: "x" }), "web", "html");
  eq(fileCategory({ fileName: "a.mp4" }), "video", "video");
  eq(fileCategory({ kind: "code", title: "x" }), "code", "code");
  eq(fileCategory({ fileName: "a.yaml" }), "code", "yaml is code");
  eq(fileCategory({ kind: "file", fileName: "a.bin", mimeType: "application/octet-stream" }), "file", "binary");
  eq(isTextual({ fileName: "a.svg" }), true, "svg is text");
  eq(isTextual({ fileName: "a.png" }), false, "png");
  eq(isTextual({ kind: "markdown", title: "x" }), true, "inline");
  eq(isTextual({ kind: "image", title: "x" }), false, "legacy image url is not a text file");
  eq(fileTypeLabel({ kind: "markdown", title: "x" }), "Markdown", "markdown label");
  eq(fileTypeLabel({ fileName: "a.png" }), "PNG", "ext label");
  eq(fileTypeLabel({ kind: "app", title: "Game" }), "App", "app label");
  eq(isPreviewableImage({ fileName: "a.svg" }), true, "svg image");
  eq(isPreviewableImage({ fileName: "a.tiff", mimeType: "image/tiff" }), false, "tiff not decodable everywhere");
});

test("a web app (site) is named by its title, labelled Web app, saved as .tar", () => {
  const site = { kind: "site", title: "Todo app v2.0", fileName: "dist", mimeType: SITE_MIME };
  eq(inferMime(site), SITE_MIME, "declared site MIME");
  eq(inferMime({ kind: "site", title: "x" }), SITE_MIME, "kind implies the MIME");
  eq(artifactFileName(site), "Todo app v2.0", "title, no extension added");
  eq(artifactFileName({ kind: "site", fileName: "apps/dist", title: "" }), "dist", "dir name without a title");
  eq(artifactExtension(site), "", "a dotted title is not an extension");
  eq(fileCategory(site), "web", "web category");
  eq(fileTypeLabel(site), "Web app", "label");
  eq(isTextual(site), false, "an archive is not text");
  eq(downloadFileName({ name: "Todo app", mimeType: SITE_MIME }), "Todo app.tar", "archive download name");
  eq(downloadFileName({ name: "a.png", mimeType: "image/png" }), "a.png", "other files keep their name");
});
