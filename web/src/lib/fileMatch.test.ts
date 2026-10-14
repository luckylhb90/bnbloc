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

eq(parseFileMatch(" .PPTX, key image/* .pptx "), { mimeTypes: ["image/*"], extensions: [".pptx", ".key"] }, "parse");
eq(parseFileMatch("  "), null, "empty is no scope");
eq(formatFileMatch({ mimeTypes: ["image/*"], extensions: [".pptx"] }), ".pptx, image/*", "format");
eq(parseFileMatch(formatFileMatch({ mimeTypes: ["text/csv"], extensions: [".csv", ".tsv"] })), { mimeTypes: ["text/csv"], extensions: [".csv", ".tsv"] }, "round trip");
