// Mermaid previewer (bnbloc.previewer.contract/v1).
//
// Draws a .mmd / .mermaid file with mermaid in strict mode (labels encoded,
// clicks and scripts off). The host hands over the file bytes; the toolbar
// (zoom, fit) is host-rendered; a source that does not parse shows
// mermaid's report with its line and no stale drawing. Clicking a box, an
// arrow's label, a group or an actor points the Ask AI panel at it, naming
// the source lines that mention it (lib/selection.ts); Compare reads the
// source's statements (src/source.ts).

import mermaid from "mermaid";

import {
  onFile,
  onInit,
  onToolbar,
  onUnits,
  provideThumbnail,
  reportError,
  setTitle,
  setToolbar,
  type ToolbarItem,
} from "../../../../web/previewer-sdk/src/index.ts";
import { renderCover } from "../../../../web/previewer-sdk/src/cover.ts";
import { keepLinksInside } from "../../lib/office.ts";
import { collapseText, quotedLabel, watchPointing } from "../../lib/selection.ts";
import { clampZoom, fitZoom } from "../../lib/zoom.ts";
import { linesNaming, nodeIdOf, parseErrorMessage, sourceUnits } from "./source.ts";

const stageEl = document.getElementById("stage") as HTMLElement;
const diagramEl = document.getElementById("diagram") as HTMLElement;
const statusEl = document.getElementById("status") as HTMLElement;

// Mermaid's own limit (maxTextSize) refuses a larger source with a message
// the member reads.
const MAX_SOURCE_CHARS = 200_000;

let theme: "light" | "dark" = "light";
let source = "";
let svg: SVGSVGElement | null = null;
let natural = { w: 0, h: 0 };
let zoom = 1;
let renderId = "";
let renderNo = 0;

function initMermaid(): void {
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    theme: theme === "dark" ? "dark" : "default",
    maxTextSize: MAX_SOURCE_CHARS,
    flowchart: { htmlLabels: true },
  });
}

// The file's card picture (capability "thumbnail"): the diagram on paper, in
// the light theme whatever the member's (the room keeps one for everyone).
// Drawn from a second render whose labels are SVG text: a label held in a
// foreignObject taints the canvas in WebKit, and the picture could not be read
// back. One per source; a diagram that will not draw leaves the card its icon.
let coverFor = "";
async function sendCover(text: string): Promise<void> {
  if (coverFor === text) return;
  coverFor = text;
  try {
    mermaid.initialize({ startOnLoad: false, securityLevel: "strict", theme: "default", maxTextSize: MAX_SOURCE_CHARS, htmlLabels: false, flowchart: { htmlLabels: false } });
    const { svg: markup } = await mermaid.render(`bnbloc-mermaid-cover-${++renderNo}`, text);
    const el = new DOMParser().parseFromString(markup, "image/svg+xml").documentElement;
    const box = (el.getAttribute("viewBox") ?? "").split(/[\s,]+/).map(Number);
    if (box.length !== 4 || !(box[2] > 0) || !(box[3] > 0)) return;
    el.setAttribute("width", String(box[2]));
    el.setAttribute("height", String(box[3]));
    el.removeAttribute("style");
    const img = new Image();
    img.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(new XMLSerializer().serializeToString(el))}`;
    await img.decode();
    const png = await renderCover((ctx, w, h) => {
      const pad = 14;
      const k = Math.min((w - 2 * pad) / box[2], (h - 2 * pad) / box[3], 2);
      ctx.drawImage(img, (w - box[2] * k) / 2, (h - box[3] * k) / 2, box[2] * k, box[3] * k);
    });
    await provideThumbnail(png);
  } catch {
    // no cover
  } finally {
    initMermaid();
  }
}

function toolbarItems(): ToolbarItem[] {
  return [
    { id: "zoom", icon: "zoom-in", label: "Zoom", kind: "zoom", value: zoom, disabled: !svg },
    { id: "fit", icon: "fit", label: "Fit to view", kind: "button", disabled: !svg },
  ];
}

function publishToolbar(): void {
  setToolbar(toolbarItems());
}

function applyZoom(): void {
  if (!svg) return;
  svg.style.width = `${Math.round(natural.w * zoom)}px`;
  svg.style.height = `${Math.round(natural.h * zoom)}px`;
}

function showStatus(message: string, error = false): void {
  statusEl.textContent = message;
  statusEl.classList.toggle("error", error);
}

function clearDrawing(): void {
  pointing.reset();
  diagramEl.replaceChildren();
  svg = null;
  natural = { w: 0, h: 0 };
}

async function draw(text: string, name: string): Promise<void> {
  clearDrawing();
  source = text;
  if (!text.trim()) {
    showStatus(`${name} is empty.`);
    publishToolbar();
    return;
  }
  initMermaid();
  const id = `bnbloc-mermaid-${++renderNo}`;
  renderId = id;
  try {
    // parse first: a failed render leaves mermaid's own error drawing in
    // the page, and the report is what the member needs.
    await mermaid.parse(text);
    const { svg: markup } = await mermaid.render(id, text);
    const doc = new DOMParser().parseFromString(markup, "image/svg+xml");
    const el = doc.documentElement;
    if (el.nodeName.toLowerCase() !== "svg" || doc.querySelector("parsererror")) throw new Error("mermaid produced no drawing");
    diagramEl.append(document.importNode(el, true));
  } catch (err) {
    document.getElementById(`d${id}`)?.remove();
    const message = parseErrorMessage(err);
    showStatus(message, true);
    reportError(message.split("\n")[0].slice(0, 400));
    publishToolbar();
    return;
  }
  const drawn = diagramEl.querySelector("svg");
  if (!(drawn instanceof SVGSVGElement)) return;
  svg = drawn;
  const box = svg.viewBox.baseVal;
  const rect = svg.getBoundingClientRect();
  natural = box && box.width > 0 ? { w: box.width, h: box.height } : { w: rect.width, h: rect.height };
  svg.removeAttribute("width");
  svg.removeAttribute("height");
  svg.style.maxWidth = "none";
  zoom = fitZoom(stageEl, natural);
  applyZoom();
  showStatus("");
  setTitle(name);
  publishToolbar();
  void sendCover(text);
}

// What a click on the drawing names. Each diagram type draws its parts as
// different elements; these are the ones with a label to point at.
const PART_SELECTOR = "g.node, g.cluster, g.edgeLabel, g.actor, g.note, text.actor, text.messageText, text.noteText, text.loopText, text.labelText";

function partNoun(el: Element): string {
  if (el.matches("g.cluster")) return "Group";
  if (el.matches("g.edgeLabel, text.messageText")) return "Label";
  if (el.matches("g.actor, text.actor")) return "Participant";
  if (el.matches("g.note, text.noteText")) return "Note";
  return "Box";
}

const pointing = watchPointing(diagramEl, (el) => {
  const target = el.closest(PART_SELECTOR);
  if (!target || !diagramEl.contains(target)) return null;
  const text = collapseText(target.textContent ?? "");
  if (!text) return null;
  const where = partNoun(target);
  const id = nodeIdOf(target.id, renderId);
  const naming = linesNaming(source, text);
  const detail = [text, id ? `Node id: ${id}` : "", ...naming.map((l) => `Source ${l}`)].filter(Boolean).join("\n");
  return { element: target as unknown as HTMLElement, where, text: detail, label: quotedLabel(where, text), kind: "block" };
});

keepLinksInside(diagramEl);

onInit((init) => {
  theme = init.theme === "dark" ? "dark" : "light";
  document.documentElement.dataset.theme = theme;
  showStatus(`Loading ${init.file.name}…`);
  publishToolbar();
});

onFile((bytes, file) => {
  draw(new TextDecoder("utf-8").decode(bytes), file.name).catch((err) => {
    const message = parseErrorMessage(err);
    showStatus(message, true);
    reportError(message.split("\n")[0].slice(0, 400));
  });
});

onToolbar((id, value) => {
  if (!svg) return;
  if (id === "zoom" && typeof value === "number") zoom = clampZoom(value);
  else if (id === "fit") zoom = fitZoom(stageEl, natural);
  else return;
  applyZoom();
  publishToolbar();
});

// Compare reads a version into its statements (capability "units").
onUnits((bytes) => sourceUnits(new TextDecoder("utf-8").decode(bytes)));
