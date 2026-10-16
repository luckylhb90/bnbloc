// The document's units for Compare (capability "units"): one per paragraph
// or table of the body, in order. Paragraphs have no identity across
// versions, so units carry no key and align by text. A unit is labelled by
// what it is and the heading it falls under ("Paragraph · Highlights",
// "Table · By region"); a heading is its own unit ("Heading · Highlights").
// A table's text is its rows, cells separated by tabs.

import { headline, openPackage, runText, type ZipReader } from "../../lib/ooxml.ts";

export interface DocUnit {
  readonly label: string;
  readonly text: string;
  /** Where it is: "t:<its first words>" (main.ts finds the rendered block
   *  that starts so; paragraphs carry no identity). */
  readonly at: string;
}

/** A block's locator: its text's start, whitespace folded. */
export function textLocator(text: string): string {
  return `t:${[...text.replace(/\s+/g, " ").trim()].slice(0, 120).join("")}`;
}

const HEADING_STYLE = /<w:pStyle\b[^>]*w:val="(Title|Heading\d|heading\d|Subtitle)"/;

function tableText(xml: string): string {
  const rows: string[] = [];
  for (const row of xml.matchAll(/<w:tr\b[\s\S]*?<\/w:tr>/g)) {
    const cells = [...row[0].matchAll(/<w:tc\b[\s\S]*?<\/w:tc>/g)].map((c) =>
      [...c[0].matchAll(/<w:p\b[^>]*?(?:\/>|>([\s\S]*?)<\/w:p>)/g)].map((p) => runText(p[1] ?? "", "w").trim()).filter(Boolean).join(" "),
    );
    rows.push(cells.join("\t"));
  }
  return rows.join("\n");
}

/** The body's top-level blocks, in order: paragraphs and tables (a table
 *  nested in a table belongs to its outer table). */
function blocks(body: string): { kind: "p" | "tbl"; xml: string }[] {
  const out: { kind: "p" | "tbl"; xml: string }[] = [];
  const re = /<(\/?)w:(tbl|p)\b[^>]*?(\/?)>/g;
  let tblDepth = 0;
  let start = -1;
  let pStart = -1;
  for (const m of body.matchAll(re)) {
    const [tag, close, name, selfClosing] = m;
    const at = m.index ?? 0;
    if (name === "tbl") {
      if (!close) {
        if (tblDepth++ === 0) start = at;
      } else if (--tblDepth === 0) {
        out.push({ kind: "tbl", xml: body.slice(start, at + tag.length) });
      }
    } else if (tblDepth === 0) {
      if (selfClosing) continue;
      if (!close) pStart = at;
      else if (pStart >= 0) {
        out.push({ kind: "p", xml: body.slice(pStart, at + tag.length) });
        pStart = -1;
      }
    }
  }
  return out;
}

export async function docUnits(zip: ZipReader, bytes: ArrayBuffer | Uint8Array): Promise<DocUnit[]> {
  const pkg = await openPackage(zip, bytes);
  const doc = await pkg.part("word/document.xml");
  if (!doc) throw new Error("not a Word document (no word/document.xml)");
  const body = /<w:body\b[^>]*>([\s\S]*)<\/w:body>/.exec(doc)?.[1] ?? "";
  const out: DocUnit[] = [];
  let heading = "";
  for (const b of blocks(body)) {
    if (b.kind === "tbl") {
      const text = tableText(b.xml);
      if (text.trim()) out.push({ label: heading ? `Table · ${heading}` : "Table", text, at: textLocator(text) });
      continue;
    }
    const text = runText(b.xml, "w").trim();
    if (!text) continue;
    if (HEADING_STYLE.test(b.xml)) {
      heading = headline(text, 40);
      out.push({ label: `Heading · ${heading}`, text, at: textLocator(text) });
    } else out.push({ label: heading ? `Paragraph · ${heading}` : "Paragraph", text, at: textLocator(text) });
  }
  return out;
}

/** The first blocks of the document as a cover's page: headings marked, a
 *  table as its first rows with the cells joined, a paragraph as it is. */
export function pageCover(units: readonly Pick<DocUnit, "label" | "text">[], maxBlocks = 12): { text: string; heading?: boolean }[] {
  const out: { text: string; heading?: boolean }[] = [];
  for (const u of units) {
    if (out.length >= maxBlocks) break;
    if (u.label.startsWith("Heading")) out.push({ text: u.text, heading: true });
    else if (u.label.startsWith("Table")) out.push({ text: u.text.split("\n").slice(0, 3).map((row) => row.split("\t").join("  ·  ")).join("\n") });
    else out.push({ text: u.text });
  }
  return out;
}
