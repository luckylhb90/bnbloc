import { SITE_MIME } from "@/lib/artifactFiles";
import { registerArtifactViewer } from "./registry";

// Built-in viewers. Every one is lazy (its code loads with the first file it
// renders) and contains untrusted content one of three ways: rendered as
// data only ("safe"), sanitized markup ("sanitized"), or inside the opaque-
// origin sandboxed runner frame ("sandboxed"). Installed previewer components
// register into the same registry at runtime (see registry.ts).

const MiB = 1024 * 1024;

/** The plain-text viewer: the host's fallback for inline text versions no
 *  other viewer claims. */
export const TEXT_VIEWER_ID = "core.text";

const CODE_EXTENSIONS = [
  "js", "mjs", "cjs", "jsx", "ts", "tsx", "css", "scss", "py", "go", "rs", "java", "kt", "c", "h", "cpp", "hpp",
  "cs", "rb", "php", "swift", "sh", "bash", "zsh", "sql", "toml", "yaml", "yml", "xml", "ini", "env", "dockerfile",
  "makefile", "gradle", "lua", "r", "dart", "vue", "svelte",
  // Also offered for files another viewer renders ("Open with… Code"):
  "html", "htm", "json", "svg", "md", "markdown", "csv", "tsv",
];

registerArtifactViewer({
  id: "core.markdown",
  label: "Markdown",
  match: { mimeTypes: ["text/markdown"], extensions: ["md", "markdown"], kinds: ["markdown"] },
  rank: 10,
  input: "text",
  safety: "sanitized",
  source: "builtin",
  cover: "text",
  maxBytes: 5 * MiB,
  load: () => import("./MarkdownRenderer"),
});

registerArtifactViewer({
  id: "core.image",
  label: "Image",
  match: {
    mimeTypes: [
      "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "image/x-icon",
      // SVG renders as an <img> from a blob URL: scripts never run, external
      // references never load, and it never becomes app DOM.
      "image/svg+xml",
      "image/*",
    ],
    extensions: ["png", "jpg", "jpeg", "gif", "webp", "avif", "bmp", "ico", "svg"],
    kinds: ["image"],
  },
  rank: 10,
  input: "blob",
  safety: "safe",
  source: "builtin",
  load: () => import("./ImageViewer"),
});

registerArtifactViewer({
  id: "core.app",
  label: "Live preview",
  match: { mimeTypes: ["text/html"], extensions: ["html", "htm"], kinds: ["app"] },
  rank: 10,
  input: "text",
  safety: "sandboxed",
  source: "builtin",
  maxBytes: 5 * MiB,
  load: () => import("./AppRenderer"),
});

// A web app snapshot runs from the gateway's site routes in a sandboxed
// iframe (opaque origin, the gateway's `sandbox` CSP); it needs no bytes.
registerArtifactViewer({
  id: "core.site",
  label: "Web app",
  match: { mimeTypes: [SITE_MIME], kinds: ["site"] },
  rank: 10,
  input: "site",
  safety: "sandboxed",
  source: "builtin",
  load: () => import("./SiteViewer"),
});

registerArtifactViewer({
  id: "core.pdf",
  label: "PDF",
  match: { mimeTypes: ["application/pdf"], extensions: ["pdf"] },
  rank: 10,
  input: "blob",
  safety: "sandboxed",
  source: "builtin",
  cover: "render",
  maxBytes: 100 * MiB,
  load: () => import("./PdfViewer"),
});

registerArtifactViewer({
  id: "core.table",
  label: "Table",
  match: { mimeTypes: ["text/csv", "text/tab-separated-values"], extensions: ["csv", "tsv"], kinds: ["table"] },
  rank: 10,
  input: "text",
  safety: "safe",
  source: "builtin",
  cover: "text",
  maxBytes: 50 * MiB,
  load: () => import("./TableRenderer"),
});

registerArtifactViewer({
  id: "core.json",
  label: "JSON tree",
  match: { mimeTypes: ["application/json"], extensions: ["json"] },
  rank: 10,
  input: "text",
  safety: "safe",
  source: "builtin",
  cover: "text",
  maxBytes: 50 * MiB,
  load: () => import("./JsonViewer"),
});

registerArtifactViewer({
  id: "core.media",
  label: "Player",
  match: {
    mimeTypes: [
      "audio/mpeg", "audio/wav", "audio/ogg", "audio/mp4", "audio/flac", "audio/webm",
      "video/mp4", "video/webm", "video/ogg", "video/quicktime",
      "audio/*", "video/*",
    ],
    extensions: ["mp3", "wav", "ogg", "m4a", "flac", "mp4", "webm", "mov"],
  },
  rank: 10,
  input: "blob",
  safety: "safe",
  source: "builtin",
  load: () => import("./MediaViewer"),
});

registerArtifactViewer({
  id: "core.code",
  label: "Code",
  match: {
    mimeTypes: [
      "text/javascript", "text/jsx", "text/typescript", "text/tsx", "text/css", "text/x-python", "text/x-go",
      "text/x-rust", "text/x-java", "text/x-kotlin", "text/x-c", "text/x-c++", "text/x-csharp", "text/x-ruby",
      "text/x-php", "text/x-swift", "text/x-shellscript", "application/xml", "application/yaml", "application/sql",
      "application/toml", "application/javascript",
    ],
    extensions: CODE_EXTENSIONS,
    // Inline "html" artifacts are source by their author's intent.
    kinds: ["code", "html"],
  },
  rank: 10,
  input: "text",
  safety: "safe",
  source: "builtin",
  cover: "text",
  maxBytes: 20 * MiB,
  load: () => import("./CodeViewer"),
});

registerArtifactViewer({
  id: TEXT_VIEWER_ID,
  label: "Plain text",
  match: { mimeTypes: ["text/plain", "text/*"], extensions: ["txt", "log", "text"] },
  rank: 20,
  input: "text",
  safety: "safe",
  source: "builtin",
  cover: "text",
  maxBytes: 20 * MiB,
  load: () => import("./CodeViewer").then((m) => ({ default: m.PlainTextViewer })),
});
