import { formatFileMatch, matchSpecificity, parseFileMatch, type FileFacts } from "./fileMatch.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const file = (name: string, mimeType: string, kind?: string): FileFacts => ({ name, mimeType, extension: name.includes(".") ? name.split(".").pop()! : "", kind });

const m = { mimeTypes: ["image/png", "image/*"], extensions: [".png", ".atlas.json"], kinds: ["image"] };
eq(matchSpecificity(m, file("a.png", "image/png")), 4, "exact mime");
eq(matchSpecificity(m, file("a.png", "")), 3, "extension");
eq(matchSpecificity(m, file("sheet.atlas.json", "application/json")), 5, "multi-part extension");
eq(matchSpecificity({ mimeTypes: ["application/json"] }, file("sheet.atlas.json", "application/json")), 4, "generic json loses to it");
eq(matchSpecificity(m, file("legacy", "", "image")), 2, "kind");
eq(matchSpecificity(m, file("a.webp", "image/webp")), 1, "wildcard");
eq(matchSpecificity(m, file("a.txt", "text/plain")), 0, "no match");

// A generic MIME (what a sniffer or an untyped upload says) ranks below an
// extension: a ".mmd" previewer beats the plain-text viewer for flow.mmd
// stored as text/plain, while the text viewer still claims it exactly.
const text = { mimeTypes: ["text/plain", "text/*"], extensions: ["txt"] };
const mermaid = { mimeTypes: ["text/vnd.mermaid"], extensions: [".mmd"] };
eq(matchSpecificity(text, file("flow.mmd", "text/plain")), 2.5, "text/plain claimed exactly is generic");
eq(matchSpecificity(mermaid, file("flow.mmd", "text/plain")), 3, "the extension claim outranks it");
eq(matchSpecificity(text, file("notes.txt", "text/plain")), 3, "its own extension still wins for .txt");
eq(matchSpecificity(text, file("README", "text/plain")), 2.5, "no extension: still the text viewer");
eq(matchSpecificity({ kinds: ["code"] }, file("Makefile", "text/plain", "code")) < matchSpecificity(text, file("Makefile", "text/plain", "code")), true, "a generic MIME still beats a kind");
eq(matchSpecificity({ mimeTypes: ["application/octet-stream"] }, file("x.bin", "application/octet-stream")), 2.5, "octet-stream is generic too");
eq(matchSpecificity(mermaid, file("flow.mmd", "text/vnd.mermaid")), 4, "a specific MIME is still exact");

eq(parseFileMatch(" .PPTX, key image/* .pptx "), { mimeTypes: ["image/*"], extensions: [".pptx", ".key"] }, "parse");
eq(parseFileMatch("  "), null, "empty is no scope");
eq(formatFileMatch({ mimeTypes: ["image/*"], extensions: [".pptx"] }), ".pptx, image/*", "format");
eq(parseFileMatch(formatFileMatch({ mimeTypes: ["text/csv"], extensions: [".csv", ".tsv"] })), { mimeTypes: ["text/csv"], extensions: [".csv", ".tsv"] }, "round trip");
