// draw.io previewer (bnbloc.previewer.contract/v1).
//
// Draws a .drawio / .dio file: every page, with its shapes, connectors and
// labels, laid out by maxGraph from draw.io's own geometry. The host hands
// over the file bytes; the toolbar (zoom, fit) is host-rendered; pages are
// tabs under the drawing. model.ts reads the file and decides what a cell is
// (and cleans every HTML label through DOMPurify before maxGraph sees it);
// shapes.ts teaches maxGraph draw.io's shapes; a click on a box or an arrow
// points the Ask AI panel at it, naming its id, text, shape and neighbours
// (units.ts); Compare reads the cells (capability "units").

import { ModelXmlSerializer, type Graph } from "@maxgraph/core";
import DOMPurify from "dompurify";

import {
  onFile,
  onInit,
  onReveal,
  onToolbar,
  onUnits,
  reportError,
  setTitle,
  setToolbar,
  type PreviewFile,
  type ToolbarItem,
} from "../../../../web/previewer-sdk/src/index.ts";
import { countedTitle, keepLinksInside, shortName } from "../../lib/office.ts";
import { pendingReveal, reveal, watchPointing } from "../../lib/selection.ts";
import { clampZoom, fitZoom } from "../../lib/zoom.ts";
import { MAX_CELLS, MAX_PAGES, graphXml, readDrawing, type Drawing } from "./model.ts";
import { createGraph } from "./shapes.ts";
import { cellIndex, cellPoint, drawingUnits, findCell, nounOf } from "./units.ts";

const stageEl = document.getElementById("stage") as HTMLElement;
const diagramEl = document.getElementById("diagram") as HTMLElement;
const tabsEl = document.getElementById("tabs") as HTMLElement;
const noteEl = document.getElementById("note") as HTMLElement;
const statusEl = document.getElementById("status") as HTMLElement;

// Blank space around the drawing, in CSS pixels at 100%.
const PAD = 16;
// How close to an arrow a click counts as on it, in CSS pixels.
const EDGE_TOLERANCE = 6;

let drawing: Drawing | null = null;
let file: PreviewFile | null = null;
let pageIndex = 0;
let graph: Graph | null = null;
let nodes = new Map<string, Element>();
let natural = { w: 0, h: 0 };
let origin = { x: 0, y: 0 };
let zoom = 1;
let generation = 0;
let lastPointer = { x: 0, y: 0 };

// The inline formatting a label may carry; no script, no form, no embed.
const LABEL_TAGS = ["a", "b", "br", "div", "em", "font", "h1", "h2", "h3", "h4", "i", "li", "ol", "p", "s", "span", "strong", "sub", "sup", "table", "tbody", "td", "th", "tr", "u", "ul"];
const LABEL_ATTRS = ["align", "color", "colspan", "face", "href", "rowspan", "size", "style"];

// Parsing hostile markup may make Chromium log a refused inline handler: the
// frame's CSP (script-src 'self') is the second wall behind DOMPurify.
function sanitize(html: string): string {
  return DOMPurify.sanitize(html, { ALLOWED_TAGS: LABEL_TAGS, ALLOWED_ATTR: LABEL_ATTRS, ALLOW_DATA_ATTR: false, ALLOWED_URI_REGEXP: /^(?:https?:|mailto:|#)/i });
}

function toolbarItems(): ToolbarItem[] {
  return [
    { id: "zoom", icon: "zoom-in", label: "Zoom", kind: "zoom", value: zoom, disabled: !graph },
    { id: "fit", icon: "fit", label: "Fit to view", kind: "button", disabled: !graph },
  ];
}

function publishToolbar(): void {
  setToolbar(toolbarItems());
}

function showStatus(message: string, error = false): void {
  statusEl.textContent = message;
  statusEl.classList.toggle("error", error);
}

function clearDrawing(): void {
  pointing.reset();
  graph?.destroy();
  graph = null;
  nodes = new Map();
  diagramEl.replaceChildren();
  diagramEl.style.width = "";
  diagramEl.style.height = "";
  natural = { w: 0, h: 0 };
}

function titleFor(): string {
  if (!file) return "";
  return drawing && drawing.pages.length > 1 ? countedTitle(file.name, "page", pageIndex, drawing.pages.length) : shortName(file.name);
}

function drawTabs(): void {
  const pages = drawing?.pages ?? [];
  tabsEl.replaceChildren(
    ...pages.map((p, i) => {
      const b = document.createElement("button");
      b.type = "button";
      b.role = "tab";
      b.textContent = p.name;
      b.setAttribute("aria-selected", String(i === pageIndex));
      b.addEventListener("click", () => showPage(i));
      return b;
    }),
  );
  tabsEl.hidden = pages.length < 2;
}

function notes(): string {
  if (!drawing) return "";
  const page = drawing.pages[pageIndex];
  const out: string[] = [];
  if (page.omitted > 0) out.push(`${page.omitted.toLocaleString("en")} cells are not drawn (a page shows at most ${MAX_CELLS.toLocaleString("en")}); download the file for the rest.`);
  if (drawing.omittedPages > 0) out.push(`${drawing.omittedPages} more pages are not shown (at most ${MAX_PAGES} are read).`);
  return out.join(" ");
}

/** Marks every cell's drawn parts with its id: pointing and Show find a cell
 *  by it (a redraw may replace a part, so this runs after each). */
function tagNodes(): void {
  if (!graph || !drawing) return;
  const model = graph.getDataModel();
  nodes = new Map();
  for (const c of drawing.pages[pageIndex].cells) {
    if (c.kind === "layer") continue;
    const cell = model.getCell(c.id);
    const state = cell ? graph.getView().getState(cell) : null;
    if (!state) continue;
    if (state.shape?.node) {
      state.shape.node.setAttribute("data-cell", c.id);
      nodes.set(c.id, state.shape.node);
    }
    state.text?.node?.setAttribute("data-cell", c.id);
  }
}

function applyZoom(): void {
  if (!graph) return;
  diagramEl.style.width = `${Math.round(natural.w * zoom)}px`;
  diagramEl.style.height = `${Math.round(natural.h * zoom)}px`;
  graph.getView().scaleAndTranslate(zoom, -origin.x, -origin.y);
  tagNodes();
}

function showPage(i: number): void {
  if (!drawing || !file) return;
  pageIndex = i;
  clearDrawing();
  drawTabs();
  noteEl.textContent = notes();
  setTitle(titleFor());
  const page = drawing.pages[i];
  if (!page.cells.some((c) => c.kind !== "layer")) {
    showStatus("This page is empty.");
    publishToolbar();
    return;
  }
  const g = createGraph(diagramEl);
  graph = g;
  try {
    new ModelXmlSerializer(g.getDataModel()).import(graphXml(page, sanitize));
    g.getView().validate();
  } catch (err) {
    clearDrawing();
    const message = `Page “${page.name}” cannot be drawn: ${err instanceof Error ? err.message : String(err)}`;
    showStatus(message, true);
    reportError(message.slice(0, 400));
    publishToolbar();
    return;
  }
  const b = g.getGraphBounds();
  origin = { x: b.x - PAD, y: b.y - PAD };
  natural = { w: Math.ceil(b.width + 2 * PAD), h: Math.ceil(b.height + 2 * PAD) };
  zoom = fitZoom(stageEl, natural);
  applyZoom();
  showStatus("");
  publishToolbar();
  reveals.apply();
}

/** The cell a click landed on: the part it hit, or the arrow near it (an
 *  arrow is a thin line, so a click beside it counts). */
function cellIdAt(el: Element): string | null {
  const hit = el.closest("[data-cell]")?.getAttribute("data-cell");
  if (hit) return hit;
  if (!graph || !diagramEl.contains(el)) return null;
  const box = diagramEl.getBoundingClientRect();
  const near = graph.getCellAt(lastPointer.x - box.left, lastPointer.y - box.top, null, false, true, undefined);
  const id = near?.getId();
  return id ?? null;
}

diagramEl.addEventListener(
  "pointerup",
  (e) => {
    lastPointer = { x: e.clientX, y: e.clientY };
  },
  true,
);

const pointing = watchPointing(diagramEl, (el) => {
  if (!drawing) return null;
  const id = cellIdAt(el);
  const cell = id ? cellIndex(drawing.pages[pageIndex]).get(id) : undefined;
  if (!id || !cell || cell.kind === "layer") return null;
  const point = cellPoint(drawing, pageIndex, cell);
  return { element: (nodes.get(id) ?? el) as HTMLElement, where: nounOf(cell), text: point.detail, label: point.label, kind: "block", keys: point.keys };
});

keepLinksInside(diagramEl);

async function open(bytes: ArrayBuffer, info: PreviewFile): Promise<void> {
  const mine = ++generation;
  let read: Drawing;
  try {
    read = await readDrawing(new TextDecoder("utf-8").decode(bytes));
  } catch (err) {
    if (mine !== generation) return;
    drawing = null;
    clearDrawing();
    tabsEl.hidden = true;
    noteEl.textContent = "";
    const message = (err instanceof Error ? err.message : String(err)).slice(0, 400);
    showStatus(message, true);
    reportError(message);
    publishToolbar();
    return;
  }
  if (mine !== generation) return;
  drawing = read;
  file = info;
  showPage(Math.min(pageIndex, read.pages.length - 1));
}

onInit((init) => {
  document.documentElement.dataset.theme = init.theme === "dark" ? "dark" : "light";
  showStatus(`Loading ${init.file.name}…`);
  publishToolbar();
});

onFile((bytes, info) => {
  open(bytes, info).catch((err) => {
    const message = (err instanceof Error ? err.message : String(err)).slice(0, 400);
    showStatus(message, true);
    reportError(message);
  });
});

onToolbar((id, value) => {
  if (!graph) return;
  if (id === "zoom" && typeof value === "number") zoom = clampZoom(value);
  else if (id === "fit") zoom = fitZoom(stageEl, natural);
  else return;
  applyZoom();
  publishToolbar();
});

// Compare reads a version into its cells (capability "units").
onUnits(async (bytes) => drawingUnits(await readDrawing(new TextDecoder("utf-8").decode(bytes))));

// "Show" a cell (its unit's at: "cell:<page id>/<cell id>"): its page drawn,
// the cell lit and in view, once the file is open.
const reveals = pendingReveal((at) => {
  if (!drawing) return false;
  const hit = findCell(drawing, at);
  if (!hit) return true;
  if (hit.page !== pageIndex) showPage(hit.page);
  reveal(nodes.get(hit.cell.id));
  return true;
});
onReveal((at) => reveals.ask(at));
