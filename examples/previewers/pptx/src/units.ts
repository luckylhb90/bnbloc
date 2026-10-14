// The deck's units for Compare (capability "units"): one per slide, in
// presentation order, keyed by the slide's id in presentation.xml (stable
// when slides move or others are added), labelled "Slide 3 · <title>". A
// slide's text is its paragraphs, its charts' values, then its speaker
// notes.

import { attributes, decodeXml, headline, openPackage, paragraphs, relationships, relsPath, type ZipReader } from "../../lib/ooxml.ts";

const NOTES_REL = "/notesSlide";
const CHART_REL = "/chart";

export interface DeckUnit {
  readonly key: string;
  readonly label: string;
  readonly text: string;
  /** Where it is in this version: "slide:<n>" (main.ts reveals it). */
  readonly at: string;
}

const PRESENTATION = "ppt/presentation.xml";

/** A slide's unit key: its id in presentation.xml. */
function slideKey(id: string | undefined, index: number): string {
  return `slide-${id ?? index}`;
}

async function slideList(pkg: Awaited<ReturnType<typeof openPackage>>): Promise<{ key: string; path: string | undefined }[]> {
  const pres = await pkg.part(PRESENTATION);
  if (!pres) throw new Error("not a PowerPoint deck (no ppt/presentation.xml)");
  const rels = relationships(await pkg.part(relsPath(PRESENTATION)), PRESENTATION);
  return [...pres.matchAll(/<p:sldId\b[^>]*\/?>/g)].map((m, i) => {
    const a = attributes(m[0]);
    return { key: slideKey(a.id, i), path: rels.get(a["r:id"] ?? "")?.target };
  });
}

/** Each slide's unit key, in order (what a click on a slide covers). */
export async function deckSlideKeys(zip: ZipReader, bytes: ArrayBuffer | Uint8Array): Promise<string[]> {
  return (await slideList(await openPackage(zip, bytes))).map((s) => s.key);
}

export async function deckUnits(zip: ZipReader, bytes: ArrayBuffer | Uint8Array): Promise<DeckUnit[]> {
  const pkg = await openPackage(zip, bytes);
  const out: DeckUnit[] = [];
  for (const [i, slide] of (await slideList(pkg)).entries()) {
    const slidePath = slide.path;
    if (!slidePath) continue;
    const lines = paragraphs((await pkg.part(slidePath)) ?? "", "a").filter((t) => t.trim() !== "");
    const slideRels = [...relationships(await pkg.part(relsPath(slidePath)), slidePath).values()];
    // A chart's numbers are its text: an edit to them is a change.
    for (const chart of slideRels.filter((r) => r.type.endsWith(CHART_REL))) {
      const values = [...((await pkg.part(chart.target)) ?? "").matchAll(/<c:v>([^<]*)<\/c:v>/g)].map((m) => decodeXml(m[1]));
      if (values.length) lines.push(`Chart: ${values.join(" ")}`);
    }
    const notesPath = slideRels.find((r) => r.type.endsWith(NOTES_REL))?.target;
    const notes = notesPath ? paragraphs((await pkg.part(notesPath)) ?? "", "a").filter((t) => t.trim() !== "") : [];
    const title = lines[0] ? headline(lines[0]) : "";
    out.push({
      key: slide.key,
      at: `slide:${i + 1}`,
      label: title ? `Slide ${i + 1} · ${title}` : `Slide ${i + 1}`,
      text: [...lines, ...(notes.length ? ["", "Notes:", ...notes] : [])].join("\n"),
    });
  }
  return out;
}
