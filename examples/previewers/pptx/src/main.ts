// PowerPoint previewer (bnbloc.previewer.contract/v1).
//
// Renders a .pptx deck as a scrolling list of slides with
// @aiden0z/pptx-renderer (HTML/SVG, charts through its ECharts build).
// Slides mount only near the viewport and decode their shapes and media on
// demand, so a long deck opens as fast as a short one. The host hands over
// the file bytes; the toolbar (zoom, fit) is host-rendered; the frame title
// follows the slide in view. Clicking a slide, or selecting text in it,
// points the Ask AI panel at it (lib/selection.ts).

import { PptxViewer, RECOMMENDED_ZIP_LIMITS } from "@aiden0z/pptx-renderer";
import JSZip from "jszip";

import {
  ZOOM_MAX,
  ZOOM_MIN,
  onFile,
  onInit,
  onToolbar,
  onReveal,
  onUnits,
  provideThumbnail,
  reportError,
  setTitle,
  setToolbar,
  type PreviewFile,
  type ToolbarItem,
} from "../../../../web/previewer-sdk/src/index.ts";
import { renderSlideCover } from "../../../../web/previewer-sdk/src/cover.ts";
import { countedTitle, keepLinksInside, sniffOffice, unreadableMessage, type OfficeKind } from "../../lib/office.ts";
import { pendingReveal, reveal, watchPointing } from "../../lib/selection.ts";
import { deckSlideKeys, deckUnits, slideCover } from "./units.ts";

const POWERPOINT: OfficeKind = { noun: "PowerPoint presentation", ext: ".pptx", legacy: ".ppt", apps: "PowerPoint or Keynote" };

const scrollEl = document.getElementById("deck") as HTMLElement;
const deckEl = document.getElementById("slides") as HTMLElement;
const statusEl = document.getElementById("status") as HTMLElement;

let viewer: PptxViewer | null = null;
// Each file opens under its own controller: a newer file aborts an older
// one still parsing, so a slow old version never replaces a newer one.
let opening: AbortController | null = null;
let fileName = "";
let zoom = 1;

function toolbarItems(): ToolbarItem[] {
  return [
    { id: "zoom", icon: "zoom-in", label: "Zoom", kind: "zoom", value: zoom },
    { id: "fit", icon: "fit", label: "Fit to width", kind: "button", disabled: !viewer },
  ];
}

function publishToolbar(): void {
  setToolbar(toolbarItems());
}

// Each slide's Compare key, read when a deck opens: a picked slide says
// which unit it covers.
let slideKeys: string[] = [];

const pointing = watchPointing(deckEl, (el) => {
  const item = el.closest<HTMLElement>("[data-slide-index]");
  if (!item || !deckEl.contains(item)) return null;
  const index = Number(item.dataset.slideIndex);
  const where = `Slide ${index + 1}`;
  const key = slideKeys[index];
  return { element: item, where, text: item.innerText, label: where, kind: "slide", ...(key ? { keys: [key] } : {}) };
});

function close(): void {
  pointing.reset();
  opening?.abort();
  opening = null;
  viewer?.destroy();
  viewer = null;
  deckEl.replaceChildren();
}

function failedSlide(index: number, error: unknown): void {
  console.error(`slide ${index + 1} could not be rendered`, error);
  const item = deckEl.querySelector(`[data-slide-index="${index}"]`);
  const panel = item?.firstElementChild;
  if (!(panel instanceof HTMLElement)) return;
  const note = document.createElement("div");
  note.className = "slide-failed";
  note.textContent = `Slide ${index + 1} could not be rendered.`;
  panel.replaceChildren(note);
}

// The file's card picture (capability "thumbnail"): the first slide's title and
// lines on a slide-shaped page, from the text the units already read; no
// rendering, so nothing to taint a canvas. Nothing to draw: the icon stays.
async function sendCover(bytes: ArrayBuffer): Promise<void> {
  const cover = slideCover((await deckUnits(JSZip, bytes))[0]);
  if (cover) await provideThumbnail(await renderSlideCover(cover));
}

async function open(bytes: ArrayBuffer, file: PreviewFile): Promise<void> {
  close();
  fileName = file.name;
  const format = sniffOffice(bytes);
  if (format !== "zip") throw new Error(unreadableMessage(POWERPOINT, format, file.name));
  slideKeys = [];
  void sendCover(bytes.slice(0)).catch(() => {});
  void deckSlideKeys(JSZip, bytes.slice(0)).then(
    (keys) => (slideKeys = keys),
    () => {},
  );
  const controller = new AbortController();
  opening = controller;
  statusEl.textContent = `Opening ${file.name}…`;
  const next = new PptxViewer(deckEl, {
    fitMode: "contain",
    zoomPercent: 100,
    scrollContainer: scrollEl,
    zipLimits: RECOMMENDED_ZIP_LIMITS,
    pdfjs: false,
    onSlideChange: (index) => setTitle(countedTitle(fileName, "slide", index, next.slideCount)),
    onSlideError: failedSlide,
  });
  try {
    await next.open(bytes, {
      renderMode: "list",
      lazySlides: true,
      lazyMedia: true,
      signal: controller.signal,
      listOptions: { windowed: true, initialSlides: 4, batchSize: 4 },
    });
  } catch (err) {
    next.destroy();
    if (controller.signal.aborted) return;
    console.error(err);
    throw new Error(unreadableMessage(POWERPOINT, "zip", file.name));
  }
  if (controller.signal.aborted) {
    next.destroy();
    return;
  }
  opening = null;
  viewer = next;
  zoom = 1;
  statusEl.textContent = next.slideCount === 0 ? `${file.name} has no slides.` : "";
  setTitle(countedTitle(file.name, "slide", 0, next.slideCount));
  publishToolbar();
  reveals.apply();
}

keepLinksInside(deckEl);

onInit((init) => {
  document.documentElement.dataset.theme = init.theme;
  statusEl.textContent = `Loading ${init.file.name}…`;
  publishToolbar();
});

onFile((bytes, file) => {
  open(bytes, file).catch((err) => {
    const message = err instanceof Error ? err.message : String(err);
    statusEl.textContent = message;
    reportError(message);
  });
});

onToolbar((id, value) => {
  if (!viewer) return;
  if (id === "zoom" && typeof value === "number") {
    zoom = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, value));
    void viewer.setZoom(Math.round(zoom * 100));
  } else if (id === "fit") {
    zoom = 1;
    void viewer.setFitMode("contain").then(() => viewer?.setZoom(100));
  } else {
    return;
  }
  publishToolbar();
});

// Compare reads a version into slides (capability "units", src/units.ts).
onUnits((bytes) => deckUnits(JSZip, bytes));

// "Show" a slide (its units' at: "slide:<n>"), once the deck is open.
const reveals = pendingReveal((at) => {
  const n = /^slide:(\d+)$/.exec(at)?.[1];
  const el = n && viewer ? deckEl.querySelector(`[data-slide-index="${Number(n) - 1}"]`) : null;
  reveal(el);
  return !!el || !n;
});
onReveal((at) => reveals.ask(at));
