// Pure file-type helpers for room artifacts (no React, no DOM), shared by the
// viewer registry, the workbench, file cards and the deliverables view, and
// unit-tested under node.
//
// An artifact version is either inline text (the original model: kind +
// optional language, no MIME) or a blob-backed file carrying `mimeType` and
// `fileName`. Every consumer asks the same questions — "what MIME is this?",
// "what extension?", "which icon/category?" — so the answers live here once.

export interface ArtifactFileFacts {
  readonly kind?: string;
  readonly title?: string;
  readonly fileName?: string;
  readonly mimeType?: string;
  readonly language?: string;
}

/** A `site` artifact version: a static web app snapshot (a tar of its
 *  directory) the gateway serves file by file to a sandboxed frame. */
export const SITE_MIME = "application/vnd.bnbloc.site+tar";

/** True for a web-app snapshot (kind `site` or the site MIME type). */
export function isSite(facts: ArtifactFileFacts): boolean {
  return facts.kind === "site" || normalizeMime(facts.mimeType) === SITE_MIME;
}

// Extension -> MIME for the types the built-in viewers care about. Kept to
// stable IANA / de-facto registrations; anything unknown stays "" and falls
// back to the artifact kind for viewer selection.
const EXT_MIME: Readonly<Record<string, string>> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  avif: "image/avif",
  bmp: "image/bmp",
  ico: "image/x-icon",
  svg: "image/svg+xml",
  pdf: "application/pdf",
  md: "text/markdown",
  markdown: "text/markdown",
  txt: "text/plain",
  log: "text/plain",
  csv: "text/csv",
  tsv: "text/tab-separated-values",
  json: "application/json",
  html: "text/html",
  htm: "text/html",
  xml: "application/xml",
  yaml: "application/yaml",
  yml: "application/yaml",
  js: "text/javascript",
  mjs: "text/javascript",
  cjs: "text/javascript",
  jsx: "text/jsx",
  ts: "text/typescript",
  tsx: "text/tsx",
  css: "text/css",
  py: "text/x-python",
  go: "text/x-go",
  rs: "text/x-rust",
  java: "text/x-java",
  kt: "text/x-kotlin",
  c: "text/x-c",
  h: "text/x-c",
  cpp: "text/x-c++",
  hpp: "text/x-c++",
  cs: "text/x-csharp",
  rb: "text/x-ruby",
  php: "text/x-php",
  swift: "text/x-swift",
  sh: "text/x-shellscript",
  bash: "text/x-shellscript",
  zsh: "text/x-shellscript",
  sql: "application/sql",
  toml: "application/toml",
  mp3: "audio/mpeg",
  wav: "audio/wav",
  ogg: "audio/ogg",
  m4a: "audio/mp4",
  flac: "audio/flac",
  mp4: "video/mp4",
  webm: "video/webm",
  mov: "video/quicktime",
  zip: "application/zip",
  gz: "application/gzip",
  tar: "application/x-tar",
};

// Language name (the inline `code` artifact's `language`) -> extension, so a
// legacy code artifact without a file name still selects by type.
const LANGUAGE_EXT: Readonly<Record<string, string>> = {
  javascript: "js",
  typescript: "ts",
  python: "py",
  golang: "go",
  go: "go",
  rust: "rs",
  shell: "sh",
  bash: "sh",
  json: "json",
  yaml: "yaml",
  html: "html",
  css: "css",
  sql: "sql",
  java: "java",
  markdown: "md",
};

// Legacy kind -> MIME for inline artifacts with no file name. Kind "html"
// is deliberately absent: an inline html artifact is SOURCE by its author's
// intent (only kind "app" is meant to run), so it selects the code viewer by
// kind; an html FILE (declared text/html or a .html name) opens in the
// sandboxed runner.
const KIND_MIME: Readonly<Record<string, string>> = {
  markdown: "text/markdown",
  app: "text/html",
  table: "text/csv",
  site: SITE_MIME,
};

/** Lower-case extension of a file name ("" when there is none). Only the last
 *  path segment counts; a leading dot (".env") is a name, not an extension. */
export function fileExtension(name: string | undefined): string {
  if (!name) return "";
  const base = name.split(/[\\/]/).pop() ?? "";
  const dot = base.lastIndexOf(".");
  if (dot <= 0 || dot === base.length - 1) return "";
  const ext = base.slice(dot + 1).toLowerCase();
  return /^[a-z0-9+_-]{1,12}$/.test(ext) ? ext : "";
}

/** Strips parameters and case from a MIME type ("Text/HTML; charset=x" ->
 *  "text/html"). */
export function normalizeMime(mime: string | undefined): string {
  return (mime ?? "").split(";")[0].trim().toLowerCase();
}

/** The name an artifact is shown and downloaded as: its file name, else its
 *  title, plus an extension derived from the kind when the title has none. */
export function artifactFileName(facts: ArtifactFileFacts): string {
  // A web app is named by its page title (else its directory); its archive
  // extension is a download detail (downloadFileName), not its name.
  if (isSite(facts)) return facts.title?.trim() || facts.fileName?.split(/[\\/]/).pop()?.trim() || "site";
  const raw = (facts.fileName || facts.title || "artifact").trim();
  const name = raw.split(/[\\/]/).pop() || "artifact";
  if (fileExtension(name)) return name;
  const ext = defaultExtension(facts);
  return ext ? `${name}.${ext}` : name;
}

function defaultExtension(facts: ArtifactFileFacts): string {
  const mime = normalizeMime(facts.mimeType);
  if (mime) {
    for (const [ext, m] of Object.entries(EXT_MIME)) if (m === mime) return ext;
  }
  if (facts.kind === "code" && facts.language) return LANGUAGE_EXT[facts.language.toLowerCase()] ?? "txt";
  switch (facts.kind) {
    case "markdown":
      return "md";
    case "html":
    case "app":
      return "html";
    case "table":
      return "csv";
    case "code":
      return "txt";
    default:
      return "";
  }
}

/** The extension viewer selection matches on: the file name's, else the
 *  title's (bridge-published titles are workspace paths), else the legacy
 *  code language's. */
export function artifactExtension(facts: ArtifactFileFacts): string {
  if (isSite(facts)) return "";
  const fromName = fileExtension(facts.fileName) || fileExtension(facts.title);
  if (fromName) return fromName;
  if (facts.kind === "code" && facts.language) return LANGUAGE_EXT[facts.language.toLowerCase()] ?? "";
  return "";
}

/** The effective MIME type: declared, else inferred from the extension, else
 *  from the legacy kind; "" when nothing is known (selection then falls back
 *  to the kind). A declared generic octet-stream yields to a known
 *  extension. */
export function inferMime(facts: ArtifactFileFacts): string {
  const declared = normalizeMime(facts.mimeType);
  if (declared && declared !== "application/octet-stream") return declared;
  const ext = artifactExtension(facts);
  if (ext && EXT_MIME[ext]) return EXT_MIME[ext];
  if (facts.kind && KIND_MIME[facts.kind]) return KIND_MIME[facts.kind];
  return declared;
}

export type FileCategory =
  | "image"
  | "pdf"
  | "markdown"
  | "code"
  | "text"
  | "table"
  | "json"
  | "web"
  | "audio"
  | "video"
  | "archive"
  | "file";

const CODE_EXT = new Set([
  "js", "mjs", "cjs", "jsx", "ts", "tsx", "css", "py", "go", "rs", "java", "kt", "c", "h", "cpp", "hpp",
  "cs", "rb", "php", "swift", "sh", "bash", "zsh", "sql", "toml", "yaml", "yml", "xml",
]);

/** A coarse category for icons, card styling and the deliverables type
 *  filter. */
export function fileCategory(facts: ArtifactFileFacts): FileCategory {
  const mime = inferMime(facts);
  const ext = artifactExtension(facts);
  if (facts.kind === "app" || facts.kind === "html" || mime === "text/html" || mime === SITE_MIME) return "web";
  if (mime.startsWith("image/") || facts.kind === "image") return "image";
  if (mime === "application/pdf") return "pdf";
  if (mime === "text/markdown") return "markdown";
  if (mime === "text/csv" || mime === "text/tab-separated-values" || facts.kind === "table") return "table";
  if (mime === "application/json") return "json";
  if (mime.startsWith("audio/")) return "audio";
  if (mime.startsWith("video/")) return "video";
  if (mime === "application/zip" || mime === "application/gzip" || mime === "application/x-tar") return "archive";
  if (facts.kind === "code" || CODE_EXT.has(ext)) return "code";
  if (mime.startsWith("text/")) return "text";
  // An inline artifact of an unrecognized kind is still text by model.
  if (!mime && facts.kind) return "text";
  return "file";
}

/** True when the bytes are meaningful as UTF-8 text (text viewers, the text
 *  diff, inline editing). */
export function isTextual(facts: ArtifactFileFacts): boolean {
  const mime = inferMime(facts);
  if (!mime) return facts.kind !== undefined && facts.kind !== "image";
  if (mime === "image/svg+xml") return true;
  return (
    mime.startsWith("text/") ||
    mime === "application/json" ||
    mime === "application/xml" ||
    mime === "application/yaml" ||
    mime === "application/sql" ||
    mime === "application/toml"
  );
}

/** Short human label for the type ("PNG", "Markdown", "PDF"). */
export function fileTypeLabel(facts: ArtifactFileFacts): string {
  const ext = artifactExtension(facts);
  const category = fileCategory(facts);
  if (category === "markdown") return "Markdown";
  if (isSite(facts)) return "Web app";
  if (facts.kind === "app") return "App";
  if (ext) return ext.toUpperCase();
  if (facts.kind) return facts.kind.charAt(0).toUpperCase() + facts.kind.slice(1);
  return "File";
}

/** Raster/vector images a card may show as their own thumbnail (the browser
 *  decodes them; SVG renders as an image, never as DOM). */
export function isPreviewableImage(facts: ArtifactFileFacts): boolean {
  const mime = inferMime(facts);
  return /^image\/(png|jpeg|gif|webp|avif|bmp|svg\+xml|x-icon)$/.test(mime);
}

/** The name a version is saved under: its display name, plus `.tar` for a
 *  web app (the bytes are the site's archive). */
export function downloadFileName(file: { readonly name: string; readonly mimeType: string }): string {
  return normalizeMime(file.mimeType) === SITE_MIME && !file.name.toLowerCase().endsWith(".tar") ? `${file.name}.tar` : file.name;
}
