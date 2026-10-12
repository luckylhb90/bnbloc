// Drafted content packages among a turn's deliverables (authoring by
// conversation without room tools): an agent drafts an expert or skill pack
// into drafts/<name>/ (component.json, INSTRUCTIONS.md, skills/<x>/SKILL.md);
// the files publish as deliverables, and "Save as expert" packs them into a
// plain ustar archive the existing create/version routes validate with the
// one server validator (internal/contentpkg). Everything here is pure.

import { CONTENT_PACKAGE_KINDS, type ContentKind } from "./contentComponents.ts";

export const MANIFEST_FILE = "component.json";

// DraftManifest is what the action needs from a drafted component.json.
export interface DraftManifest {
  readonly kind: ContentKind;
  readonly name: string;
  readonly displayName?: string;
}

// parseDraftManifest reads a component.json of a content kind, or null.
export function parseDraftManifest(text: string): DraftManifest | null {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return null;
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const { kind, name, displayName } = raw as Record<string, unknown>;
  const short = typeof kind === "string" ? CONTENT_PACKAGE_KINDS[kind] : undefined;
  if (!short || typeof name !== "string" || !name.trim()) return null;
  return { kind: short, name: name.trim(), displayName: typeof displayName === "string" && displayName.trim() ? displayName.trim() : undefined };
}

// isManifestPath: a deliverable that may be a package's component.json
// (the artifact title is its workspace-relative path).
export function isManifestPath(title: string): boolean {
  return title === MANIFEST_FILE || title.endsWith("/" + MANIFEST_FILE);
}

// packageDir is the package directory of a component.json path ("" at the
// workspace root).
export function packageDir(manifestPath: string): string {
  const i = manifestPath.lastIndexOf("/");
  return i < 0 ? "" : manifestPath.slice(0, i);
}

// A room artifact as the gathering sees it.
export interface DraftArtifact {
  readonly artifactId: string;
  readonly title: string;
  readonly currentVersion: number;
  readonly updatedAtMs: number;
}

// DraftFile is one package file: its artifact (latest version) and its path
// inside the package.
export interface DraftFile<A extends DraftArtifact = DraftArtifact> {
  readonly artifact: A;
  readonly path: string;
}

// packageFiles gathers the room artifacts under dir (latest version of
// each; when several artifacts share a path — two sessions wrote the same
// draft — the most recently updated wins), sorted by path.
export function packageFiles<A extends DraftArtifact>(artifacts: readonly A[], dir: string): DraftFile<A>[] {
  const prefix = dir ? dir + "/" : "";
  const byPath = new Map<string, A>();
  for (const a of artifacts) {
    if (!a.title.startsWith(prefix)) continue;
    const rel = a.title.slice(prefix.length);
    if (!rel || rel.startsWith("/") || rel.split("/").some((seg) => seg === "" || seg === "." || seg === "..")) continue;
    const prev = byPath.get(rel);
    if (!prev || a.updatedAtMs > prev.updatedAtMs) byPath.set(rel, a);
  }
  return [...byPath.entries()].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([path, artifact]) => ({ artifact, path }));
}

// ---- a minimal ustar writer (regular files only, as internal/tarbundle
// expects: no PAX/GNU extensions)

const BLOCK = 512;
const enc = new TextEncoder();

function writeString(buf: Uint8Array, offset: number, size: number, value: string): void {
  const bytes = enc.encode(value);
  if (bytes.length > size) throw new Error(`tar: "${value}" does not fit ${size} bytes`);
  buf.set(bytes, offset);
}

// writeOctal writes n as zero-padded octal of size-1 digits plus NUL (Go's
// archive/tar layout).
function writeOctal(buf: Uint8Array, offset: number, size: number, n: number): void {
  const s = Math.floor(n).toString(8).padStart(size - 1, "0");
  if (s.length > size - 1) throw new Error(`tar: ${n} does not fit a ${size}-byte field`);
  writeString(buf, offset, size, s + "\0");
}

// splitUstarName splits a path into ustar (prefix, name): name ≤ 100 bytes,
// prefix ≤ 155 bytes, split at a "/".
export function splitUstarName(path: string): { prefix: string; name: string } {
  if (enc.encode(path).length <= 100) return { prefix: "", name: path };
  for (let i = path.indexOf("/"); i >= 0; i = path.indexOf("/", i + 1)) {
    const prefix = path.slice(0, i);
    const name = path.slice(i + 1);
    if (enc.encode(prefix).length <= 155 && enc.encode(name).length <= 100 && name) return { prefix, name };
  }
  throw new Error(`tar: path too long: ${path}`);
}

// tarHeader builds one regular-file ustar header.
export function tarHeader(path: string, size: number, mtimeSec = 0): Uint8Array {
  const h = new Uint8Array(BLOCK);
  const { prefix, name } = splitUstarName(path);
  writeString(h, 0, 100, name);
  writeOctal(h, 100, 8, 0o644);
  writeOctal(h, 108, 8, 0);
  writeOctal(h, 116, 8, 0);
  writeOctal(h, 124, 12, size);
  writeOctal(h, 136, 12, mtimeSec);
  h.fill(0x20, 148, 156); // checksum field counts as spaces
  h[156] = 0x30; // '0': regular file
  writeString(h, 257, 6, "ustar\0");
  writeString(h, 263, 2, "00");
  writeString(h, 345, 155, prefix);
  let sum = 0;
  for (const b of h) sum += b;
  writeString(h, 148, 8, sum.toString(8).padStart(6, "0") + "\0 ");
  return h;
}

export interface TarEntry {
  readonly path: string;
  readonly data: Uint8Array;
}

// tarArchive packs files (sorted by path) into a ustar archive: a header
// and the content padded to 512 bytes per file, then two zero blocks.
export function tarArchive(entries: readonly TarEntry[]): Uint8Array<ArrayBuffer> {
  const sorted = [...entries].sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  let total = 2 * BLOCK;
  for (const e of sorted) total += BLOCK + Math.ceil(e.data.length / BLOCK) * BLOCK;
  const out = new Uint8Array(total);
  let off = 0;
  for (const e of sorted) {
    out.set(tarHeader(e.path, e.data.length), off);
    off += BLOCK;
    out.set(e.data, off);
    off += Math.ceil(e.data.length / BLOCK) * BLOCK;
  }
  return out;
}
