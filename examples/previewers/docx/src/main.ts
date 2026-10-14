// Word previewer (bnbloc.previewer.contract/v1).
//
// Renders a .docx as pages with docx-preview (paragraph and character
// styles, tables, images, lists, headers/footers, footnotes, page breaks).
// The host hands over the file bytes; the toolbar (zoom, fit) is
// host-rendered; the frame title follows the page in view. Clicking a
// paragraph, a table or a page, or selecting text, points the Ask AI panel
// at it (lib/selection.ts).

import { renderAsync } from "docx-preview";
import JSZip from "jszip";

import {
  ZOOM_MAX,
  ZOOM_MIN,
  onFile,
  onInit,
  onResize,
  onToolbar,
  onUnits,
  reportError,
  setTitle,
  setToolbar,
  type PreviewFile,
  type ToolbarItem,
} from "../../../../web/previewer-sdk/src/index.ts";
import { unicodeBullets } from "./bullets.ts";
import { docUnits } from "./units.ts";
import { countedTitle, keepLinksInside, sniffOffice, unreadableMessage, type OfficeKind } from "../../lib/office.ts";
import { quotedLabel, tableText, watchPointing } from "../../lib/selection.ts";

const WORD: OfficeKind = { noun: "Word document", ext: ".docx", legacy: ".doc", apps: "Word or Pages" };

const scrollEl = document.getElementById("doc") as HTMLElement;
const pagesEl = document.getElementById("pages") as HTMLElement;
const statusEl = document.getElementById("status") as HTMLElement;

// A newer file supersedes an older one still rendering: each render runs
// into its own detached container and lands only if it is still current.
let generation = 0;
let fileName = "";
let pages: HTMLElement[] = [];
let observer: IntersectionObserver | null = null;
/** null = fit the widest page to the frame; else a zoom factor (1 = 100%). */
let zoom: number | null = null;

function pageWidth(): number {
  return Math.max(1, ...pages.map((p) => p.offsetWidth));
}

/** The factor that fits the widest page (plus a margin) to the frame. */
function fitFactor(): number {
  return Math.min(1, Math.max(ZOOM_MIN, (scrollEl.clientWidth - 32) / pageWidth()));
}

function effectiveZoom(): number {
  return zoom ?? fitFactor();
}

function applyZoom(): void {
  pagesEl.style.zoom = String(effectiveZoom());
  publishToolbar();
}

function toolbarItems(): ToolbarItem[] {
  return [
    { id: "zoom", icon: "zoom-in", label: "Zoom", kind: "zoom", value: Number(effectiveZoom().toFixed(4)) },
    { id: "fit", icon: "fit", label: "Fit to width", kind: "button", disabled: pages.length === 0 },
  ];
}

function publishToolbar(): void {
  setToolbar(toolbarItems());
}

/** Titles the frame with the page nearest the top of the view. */
function trackPages(): void {
  observer?.disconnect();
  const visible = new Set<number>();
  observer = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        const i = pages.indexOf(e.target as HTMLElement);
        if (e.isIntersecting) visible.add(i);
        else visible.delete(i);
      }
      if (visible.size > 0) setTitle(countedTitle(fileName, "page", Math.min(...visible), pages.length));
    },
    { root: scrollEl },
  );
  for (const p of pages) observer.observe(p);
}

// The blocks a click picks, innermost first: a table cell's paragraph
// picks the paragraph, the table's border the table.
const BLOCKS = "p, li, h1, h2, h3, h4, h5, h6, table";

const pointing = watchPointing(pagesEl, (el) => {
  const page = el.closest<HTMLElement>("section.docx");
  if (!page || !pagesEl.contains(page)) return null;
  const where = `Page ${pages.indexOf(page) + 1}`;
  const block = el.closest<HTMLElement>(BLOCKS);
  if (!block || !page.contains(block) || !block.innerText.trim()) return { element: page, where, text: page.innerText, label: where };
  if (block instanceof HTMLTableElement) {
    const text = tableText(block);
    return { element: block, where, text, label: quotedLabel(`${where}, table`, text) };
  }
  return { element: block, where, text: block.innerText };
});

async function open(bytes: ArrayBuffer, file: PreviewFile): Promise<void> {
  const mine = ++generation;
  pointing.reset();
  fileName = file.name;
  const format = sniffOffice(bytes);
  if (format !== "zip") throw new Error(unreadableMessage(WORD, format, file.name));
  statusEl.textContent = `Opening ${file.name}…`;
  const next = document.createElement("div");
  try {
    await renderAsync(new Blob([bytes]), next, undefined, {
      inWrapper: true,
      breakPages: true,
      ignoreLastRenderedPageBreak: true,
      renderHeaders: true,
      renderFooters: true,
      renderFootnotes: true,
      renderEndnotes: true,
      // Off: alternate chunks embed foreign HTML; comments and tracked
      // changes are experimental and clutter a reading view.
      renderAltChunks: false,
      renderComments: false,
      renderChanges: false,
      experimental: false,
    });
  } catch (err) {
    if (mine !== generation) return;
    console.error(err);
    throw new Error(unreadableMessage(WORD, "zip", file.name));
  }
  if (mine !== generation) return;
  for (const style of next.querySelectorAll("style")) style.textContent = unicodeBullets(style.textContent ?? "");
  pagesEl.replaceChildren(...next.childNodes);
  pages = [...pagesEl.querySelectorAll<HTMLElement>("section.docx")];
  scrollEl.scrollTop = 0;
  statusEl.textContent = pages.length === 0 ? `${file.name} is empty.` : "";
  setTitle(countedTitle(file.name, "page", 0, pages.length));
  trackPages();
  applyZoom();
}

keepLinksInside(pagesEl);

onInit((init) => {
  document.documentElement.dataset.theme = init.theme;
  statusEl.textContent = `Loading ${init.file.name}…`;
  publishToolbar();
});

onFile((bytes, file) => {
  open(bytes, file).catch((err) => {
    const message = err instanceof Error ? err.message : String(err);
    pagesEl.replaceChildren();
    pages = [];
    statusEl.textContent = message;
    reportError(message);
  });
});

onToolbar((id, value) => {
  if (id === "zoom" && typeof value === "number") zoom = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, value));
  else if (id === "fit") zoom = null;
  else return;
  applyZoom();
});

// Fit follows the frame's width; an explicit zoom stays put.
onResize(() => {
  if (zoom === null) applyZoom();
});

// Compare reads a version into paragraphs and tables (capability "units",
// src/units.ts).
onUnits((bytes) => docUnits(JSZip, bytes));
