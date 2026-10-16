package store

import (
	"net/http"
	"path"
	"strings"
)

// File typing for artifact versions, shared by the publisher (bridge) and the
// room service so a file is typed the same way on both sides. The tables are
// file-format facts (extension and magic-byte conventions), not deployment
// data.

// ArtifactInlineMaxBytes bounds one inline (text) version. Larger or binary
// content is published as a blob version under the deployment's blob cap.
const ArtifactInlineMaxBytes = 2 << 20 // 2 MiB

// artifactTextTypes are the extensions published as inline text, with the
// MIME type an inline version of that file carries.
var artifactTextTypes = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown",
	".html": "text/html", ".htm": "text/html",
	".css": "text/css", ".scss": "text/x-scss", ".less": "text/x-less",
	".js": "text/javascript", ".mjs": "text/javascript", ".cjs": "text/javascript", ".jsx": "text/javascript",
	".ts": "text/x-typescript", ".tsx": "text/x-typescript",
	".json": "application/json", ".csv": "text/csv", ".tsv": "text/tab-separated-values",
	".yaml": "text/yaml", ".yml": "text/yaml", ".toml": "text/x-toml", ".xml": "text/xml",
	".txt": "text/plain", ".log": "text/plain", ".ini": "text/plain", ".cfg": "text/plain", ".conf": "text/plain",
	".go": "text/x-go", ".py": "text/x-python", ".rs": "text/x-rust", ".java": "text/x-java",
	".kt": "text/x-kotlin", ".swift": "text/x-swift", ".scala": "text/x-scala",
	".c": "text/x-c", ".h": "text/x-c", ".cc": "text/x-c++", ".cpp": "text/x-c++", ".hpp": "text/x-c++",
	".cs": "text/x-csharp", ".rb": "text/x-ruby", ".php": "text/x-php", ".lua": "text/x-lua",
	".r": "text/x-r", ".pl": "text/x-perl", ".sql": "text/x-sql", ".sh": "text/x-shellscript",
	".vue": "text/plain", ".svelte": "text/plain", ".proto": "text/plain", ".graphql": "text/plain",
	".tf": "text/plain",
	// Diagram sources: typed by name, so a previewer that claims the MIME wins
	// over the plain-text viewer that claims whatever the sniffer said.
	".mmd": "text/vnd.mermaid", ".mermaid": "text/vnd.mermaid",
	".drawio": "application/vnd.jgraph.mxfile", ".dio": "application/vnd.jgraph.mxfile",
}

// artifactTextNames are extensionless files published as text.
var artifactTextNames = map[string]bool{
	"makefile": true, "dockerfile": true, "license": true, "readme": true,
}

// artifactBinaryTypes maps binary file extensions to their MIME type.
var artifactBinaryTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".svg": "image/svg+xml", ".bmp": "image/bmp", ".ico": "image/x-icon",
	".avif": "image/avif", ".pdf": "application/pdf", ".zip": "application/zip", ".tar": "application/x-tar",
	".gz": "application/gzip", ".tgz": "application/gzip", ".wasm": "application/wasm",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".m4a": "audio/mp4",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
}

// inlineSafeImageTypes are the raster types served inline (rendered by the
// browser) — only after the stored bytes were sniff-verified at publish.
var inlineSafeImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// MediaType lowercases a content type and strips its parameters.
func MediaType(ct string) string {
	ct = strings.TrimSpace(strings.ToLower(ct))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// IsInlineSafeImage reports a raster type that may render inline.
func IsInlineSafeImage(mimeType string) bool { return inlineSafeImageTypes[MediaType(mimeType)] }

// ArtifactTextMIME reports whether name is a known text file and the MIME
// type its inline version carries.
func ArtifactTextMIME(name string) (string, bool) {
	ext := strings.ToLower(path.Ext(name))
	if m, ok := artifactTextTypes[ext]; ok {
		return m, true
	}
	if ext == "" && artifactTextNames[strings.ToLower(path.Base(name))] {
		return "text/plain", true
	}
	return "", false
}

// ArtifactMIME types a file from its name, falling back to the content
// sniffer over its first bytes (head may be nil).
func ArtifactMIME(name string, head []byte) string {
	if m, ok := ArtifactTextMIME(name); ok {
		return m
	}
	if m, ok := artifactBinaryTypes[strings.ToLower(path.Ext(name))]; ok {
		return m
	}
	if len(head) == 0 {
		return "application/octet-stream"
	}
	return MediaType(http.DetectContentType(head))
}

// ArtifactKindForFile maps a file to its artifact kind: markdown / html /
// code for known text files, image for image types, file for everything else.
// (html is upgraded to app by the publisher when the document carries script.)
func ArtifactKindForFile(name, mimeType string) string {
	if m, ok := ArtifactTextMIME(name); ok {
		switch m {
		case "text/markdown":
			return ArtifactKindMarkdown
		case "text/html":
			return ArtifactKindHTML
		}
		return ArtifactKindCode
	}
	if strings.HasPrefix(MediaType(mimeType), "image/") {
		return ArtifactKindImage
	}
	return ArtifactKindFile
}

// ArtifactInlineMIME is the MIME type of an inline version that does not
// declare one (versions published before typed versions): derived from the
// artifact's file name or title, then its kind.
func ArtifactInlineMIME(a Artifact) string {
	for _, name := range []string{a.FileName, a.Title} {
		if m, ok := ArtifactTextMIME(name); ok {
			return m
		}
	}
	switch a.Kind {
	case ArtifactKindMarkdown:
		return "text/markdown"
	case ArtifactKindHTML, ArtifactKindApp:
		return "text/html"
	}
	return "text/plain"
}
