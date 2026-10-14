// Reading the text of an Office Open XML package (a .pptx or .docx) for
// Compare (capability "units", docs/prd/2026-10-14-file-prompts-and-compare.md
// rule 7): the zip's parts and the text of their paragraphs. A scanner over
// the XML, not a DOM, so it runs the same in the frame and under node tests;
// each previewer decides what its units are (pptx/src/units.ts,
// docx/src/units.ts).

export interface Package {
  /** A part's text; null when the package has no such part. */
  part(path: string): Promise<string | null>;
}

/** The zip reader a previewer brings (its own jszip: this folder has no
 *  dependencies of its own). */
export interface ZipReader {
  loadAsync(data: ArrayBuffer | Uint8Array): Promise<{ file(path: string): { async(type: "string"): Promise<string> } | null }>;
}

export async function openPackage(zipReader: ZipReader, bytes: ArrayBuffer | Uint8Array): Promise<Package> {
  const zip = await zipReader.loadAsync(bytes);
  return {
    async part(path) {
      const f = zip.file(path.replace(/^\//, ""));
      return f ? f.async("string") : null;
    },
  };
}

const ENTITIES: Readonly<Record<string, string>> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'" };

/** XML text with its entities decoded. */
export function decodeXml(s: string): string {
  return s.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (m, e: string) => {
    if (e[0] === "#") {
      const code = e[1] === "x" || e[1] === "X" ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10);
      return Number.isFinite(code) ? String.fromCodePoint(code) : m;
    }
    return ENTITIES[e] ?? m;
  });
}

/** An element's attributes ("id", "r:id") from its start tag. */
export function attributes(tag: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of tag.matchAll(/([\w:.-]+)\s*=\s*"([^"]*)"/g)) out[m[1]] = decodeXml(m[2]);
  return out;
}

/** The relationships of a part (its _rels file): id → target, resolved
 *  against the part's folder ("slides/slide1.xml" from ppt/presentation.xml
 *  is "ppt/slides/slide1.xml"), and id → type. */
export function relationships(relsXml: string | null, partPath: string): Map<string, { target: string; type: string }> {
  const out = new Map<string, { target: string; type: string }>();
  if (!relsXml) return out;
  const folder = partPath.includes("/") ? partPath.slice(0, partPath.lastIndexOf("/") + 1) : "";
  for (const m of relsXml.matchAll(/<Relationship\b[^>]*>/g)) {
    const a = attributes(m[0]);
    if (!a.Id || !a.Target || a.TargetMode === "External") continue;
    out.set(a.Id, { target: resolvePath(folder, a.Target), type: a.Type ?? "" });
  }
  return out;
}

/** The _rels path of a part ("ppt/slides/slide1.xml" →
 *  "ppt/slides/_rels/slide1.xml.rels"). */
export function relsPath(partPath: string): string {
  const at = partPath.lastIndexOf("/");
  return `${partPath.slice(0, at + 1)}_rels/${partPath.slice(at + 1)}.rels`;
}

function resolvePath(folder: string, target: string): string {
  const parts = (target.startsWith("/") ? target.slice(1) : folder + target).split("/");
  const out: string[] = [];
  for (const p of parts) {
    if (p === "..") out.pop();
    else if (p !== "." && p !== "") out.push(p);
  }
  return out.join("/");
}

/** The text of each paragraph (`<{ns}:p>`) in xml, in order: its runs
 *  (`<{ns}:t>`), tabs and breaks; fields (a slide number) left out. Empty
 *  paragraphs are kept as "" so callers can skip or count them. */
export function paragraphs(xml: string, ns: "a" | "w"): string[] {
  const body = xml.replace(new RegExp(`<${ns}:fld\\b[\\s\\S]*?</${ns}:fld>`, "g"), "");
  const out: string[] = [];
  for (const m of body.matchAll(new RegExp(`<${ns}:p\\b[^>]*?(?:/>|>([\\s\\S]*?)</${ns}:p>)`, "g"))) {
    out.push(runText(m[1] ?? "", ns));
  }
  return out;
}

/** The text of runs: `<{ns}:t>` contents, `<{ns}:tab/>` as a tab and
 *  `<{ns}:br/>` as a line break. */
export function runText(xml: string, ns: "a" | "w"): string {
  let out = "";
  for (const m of xml.matchAll(new RegExp(`<${ns}:t(?:\\s[^>]*)?>([^<]*)</${ns}:t>|<${ns}:(tab|br)\\b[^>]*/>`, "g"))) {
    if (m[2] === "tab") out += "\t";
    else if (m[2] === "br") out += "\n";
    else out += decodeXml(m[1]);
  }
  return out;
}

/** A short title from a text: its first line, cut. */
export function headline(text: string, max = 60): string {
  const line = text.split("\n")[0].trim();
  return [...line].length > max ? `${[...line].slice(0, max - 1).join("")}…` : line;
}
