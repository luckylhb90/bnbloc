import {
  isManifestPath,
  packageDir,
  packageFiles,
  parseDraftManifest,
  splitUstarName,
  tarArchive,
  tarHeader,
} from "./contentDrafts.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// A drafted component.json of a content kind is recognized; other JSON is not.
eq(
  parseDraftManifest(JSON.stringify({ kind: "bnbloc.expert.package/v1", name: "reviewer", displayName: "Reviewer", version: "1.0.0" })),
  { kind: "expert", name: "reviewer", displayName: "Reviewer" },
  "expert manifest",
);
eq(parseDraftManifest(JSON.stringify({ kind: "bnbloc.skill.package/v1", name: "csv" })), { kind: "skill", name: "csv" }, "skill manifest");
eq(parseDraftManifest(JSON.stringify({ kind: "bnbloc.plugin.package/v1", name: "x" })), null, "an executable kind is not a content draft");
eq(parseDraftManifest(JSON.stringify({ name: "x" })), null, "no kind");
eq(parseDraftManifest(JSON.stringify({ kind: "bnbloc.expert.package/v1" })), null, "no name");
eq(parseDraftManifest("not json"), null, "not JSON");
eq(parseDraftManifest("[1]"), null, "an array");

assert(isManifestPath("drafts/reviewer/component.json"), "nested manifest");
assert(isManifestPath("component.json"), "root manifest");
assert(!isManifestPath("drafts/reviewer/xcomponent.json"), "a suffix is not the manifest");
eq(packageDir("drafts/reviewer/component.json"), "drafts/reviewer", "package dir");
eq(packageDir("component.json"), "", "root package dir");

// Gathering: only files under the dir, paths relative to it, the most
// recently updated artifact wins a shared path, sorted.
const art = (artifactId: string, title: string, updatedAtMs: number) => ({ artifactId, title, currentVersion: 1, updatedAtMs });
const files = packageFiles(
  [
    art("a1", "drafts/reviewer/component.json", 5),
    art("a2", "drafts/reviewer/INSTRUCTIONS.md", 5),
    art("a3", "drafts/reviewer/skills/diff/SKILL.md", 5),
    art("a4", "drafts/reviewer-old/INSTRUCTIONS.md", 9),
    art("a5", "notes.md", 9),
    art("a6", "drafts/reviewer/INSTRUCTIONS.md", 7),
    art("a7", "drafts/reviewer/../escape.md", 9),
  ],
  "drafts/reviewer",
);
eq(
  files.map((f) => [f.path, f.artifact.artifactId]),
  [
    ["INSTRUCTIONS.md", "a6"],
    ["component.json", "a1"],
    ["skills/diff/SKILL.md", "a3"],
  ],
  "package files",
);
eq(packageFiles([art("r", "component.json", 1), art("s", "skills/x/SKILL.md", 1)], "").length, 2, "root package");

// ustar headers: name, octal size, magic, a checksum Go's reader accepts
// (the unsigned byte sum with the field read as spaces).
const h = tarHeader("skills/diff/SKILL.md", 10);
eq(h.length, 512, "header block");
const text = (from: number, len: number) => new TextDecoder().decode(h.slice(from, from + len)).replace(/\0+$/, "");
eq(text(0, 100), "skills/diff/SKILL.md", "name");
eq(text(124, 12), "00000000012", "size in octal");
eq(text(257, 6), "ustar", "magic");
eq(text(263, 2), "00", "version");
eq(String.fromCharCode(h[156]), "0", "regular file");
let sum = 0;
for (let i = 0; i < 512; i++) sum += i >= 148 && i < 156 ? 0x20 : h[i];
eq(Number.parseInt(text(148, 6), 8), sum, "checksum");
eq([h[154], h[155]], [0, 0x20], "checksum terminator");

// A long path splits into prefix + name at a slash.
const long = "a".repeat(80) + "/" + "b".repeat(60) + "/SKILL.md";
const split = splitUstarName(long);
eq(split.prefix + "/" + split.name, long, "split rejoins");
assert(split.name.length <= 100 && split.prefix.length <= 155, "split fits");

// The archive: header + content padded to 512 per file, then two zero blocks.
const data = new TextEncoder().encode("hello");
const tar = tarArchive([
  { path: "b.md", data },
  { path: "a.md", data: new Uint8Array(0) },
]);
eq(tar.length, 512 + 512 + 512 + 1024, "archive size");
eq(new TextDecoder().decode(tar.slice(0, 4)), "a.md", "sorted by path");
eq(new TextDecoder().decode(tar.slice(1024, 1029)), "hello", "content after its header");
assert(tar.slice(tar.length - 1024).every((b) => b === 0), "two zero blocks end it");
