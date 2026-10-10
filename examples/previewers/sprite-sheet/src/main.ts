// Sprite-sheet reference previewer (bnbloc.previewer.contract/v1).
//
// Renders a plain image (png/jpeg/gif/webp) or a sprite-sheet / atlas JSON on
// a canvas with zoom, fit, grid, checkerboard background and frame-boundary
// layers. Everything crosses the frame boundary through the SDK: the host
// hands over the file bytes, the toolbar is host-rendered (preview.toolbar),
// layer toggles are shared with the room (live state), and the first render
// produces a PNG thumbnail for file cards.

import {
  ZOOM_MAX,
  ZOOM_MIN,
  onFile,
  onInit,
  onResize,
  onState,
  onToolbar,
  provideThumbnail,
  reportError,
  sendState,
  setTitle,
  setToolbar,
  type PreviewFile,
  type Theme,
  type ToolbarItem,
} from "../../../../web/previewer-sdk/src/index.ts";
import { cellSize, isAtlasFile, parseAtlas, type AtlasFrame } from "./atlas.ts";

/** The live-state shape shared with the room: which layers are on. */
interface Layers {
  grid: boolean;
  frames: boolean;
  background: boolean;
}

interface Sheet {
  readonly width: number;
  readonly height: number;
  readonly image: ImageBitmap | null;
  readonly frames: readonly AtlasFrame[];
}

const canvas = document.getElementById("view") as HTMLCanvasElement;
const ctx = canvas.getContext("2d") as CanvasRenderingContext2D;
const statusEl = document.getElementById("status") as HTMLElement;
const framesEl = document.getElementById("frames") as HTMLElement;
const framesTitle = document.getElementById("frames-title") as HTMLElement;
const framesList = document.getElementById("frames-list") as HTMLOListElement;

let theme: Theme = "light";
let sheet: Sheet | null = null;
let fileName = "";
let zoom = 1;
let panX = 0;
let panY = 0;
let thumbnailSent = false;
const layers: Layers = { grid: false, frames: true, background: true };

const palette = () =>
  theme === "dark"
    ? { bg: "#161615", checkA: "#2a2a28", checkB: "#222220", grid: "rgba(255,255,255,0.18)", frame: "#4cc2ff", label: "#ededea" }
    : { bg: "#f6f6f4", checkA: "#ffffff", checkB: "#e6e6e3", grid: "rgba(0,0,0,0.18)", frame: "#0a74d9", label: "#1d1d1b" };

function clampZoom(value: number): number {
  return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, value));
}

// --- toolbar ---------------------------------------------------------------

function toolbarItems(): ToolbarItem[] {
  const atlas = (sheet?.frames.length ?? 0) > 0;
  return [
    { id: "zoom", icon: "zoom-in", label: "Zoom", kind: "zoom", value: Number(zoom.toFixed(4)) },
    { id: "fit", icon: "fit", label: "Fit to view", kind: "button", disabled: !sheet },
    { id: "grid", icon: "grid", label: zoom >= 4 ? "Pixel grid" : "Grid", kind: "toggle", active: layers.grid, disabled: !sheet },
    { id: "background", icon: "background", label: "Checkerboard", kind: "toggle", active: layers.background },
    { id: "frames", icon: "frames", label: "Frame boundaries", kind: "toggle", active: layers.frames, disabled: !atlas },
  ];
}

let toolbarQueued = false;
function publishToolbar(): void {
  if (toolbarQueued) return;
  toolbarQueued = true;
  requestAnimationFrame(() => {
    toolbarQueued = false;
    setToolbar(toolbarItems());
  });
}

// --- view ------------------------------------------------------------------

function viewport(): { w: number; h: number } {
  return { w: canvas.clientWidth || window.innerWidth, h: canvas.clientHeight || window.innerHeight };
}

function fit(): void {
  if (!sheet) return;
  const { w, h } = viewport();
  zoom = clampZoom(Math.min(w / sheet.width, h / sheet.height) * 0.9);
  panX = (w - sheet.width * zoom) / 2;
  panY = (h - sheet.height * zoom) / 2;
}

/** Zooms to `next` keeping the sheet point under (cx, cy) fixed. */
function zoomAt(next: number, cx: number, cy: number): void {
  const target = clampZoom(next);
  panX = cx - ((cx - panX) / zoom) * target;
  panY = cy - ((cy - panY) / zoom) * target;
  zoom = target;
}

function checkerboard(c: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, cell: number): void {
  const p = palette();
  c.fillStyle = p.checkA;
  c.fillRect(x, y, w, h);
  c.fillStyle = p.checkB;
  for (let row = 0; row * cell < h; row++) {
    for (let col = row % 2; col * cell < w; col += 2) {
      c.fillRect(x + col * cell, y + row * cell, Math.min(cell, w - col * cell), Math.min(cell, h - row * cell));
    }
  }
}

/** Draws the sheet into `c` at scale `s` with origin (ox, oy). */
function drawSheet(c: CanvasRenderingContext2D, s: Sheet, scale: number, ox: number, oy: number, withLabels: boolean): void {
  const p = palette();
  const w = s.width * scale;
  const h = s.height * scale;
  if (layers.background) checkerboard(c, ox, oy, w, h, 8);
  if (s.image) {
    c.imageSmoothingEnabled = scale < 1;
    c.drawImage(s.image, ox, oy, w, h);
  }
  if (layers.grid) {
    const step = scale >= 4 ? 1 : cellSize(s.width, s.height);
    if (step * scale >= 3) {
      c.strokeStyle = p.grid;
      c.lineWidth = 1;
      c.beginPath();
      for (let x = 0; x <= s.width; x += step) {
        c.moveTo(Math.round(ox + x * scale) + 0.5, oy);
        c.lineTo(Math.round(ox + x * scale) + 0.5, oy + h);
      }
      for (let y = 0; y <= s.height; y += step) {
        c.moveTo(ox, Math.round(oy + y * scale) + 0.5);
        c.lineTo(ox + w, Math.round(oy + y * scale) + 0.5);
      }
      c.stroke();
    }
  }
  if (layers.frames && s.frames.length > 0) {
    c.strokeStyle = p.frame;
    c.lineWidth = 1;
    c.font = "11px system-ui, sans-serif";
    c.fillStyle = p.label;
    for (const f of s.frames) {
      c.strokeRect(Math.round(ox + f.x * scale) + 0.5, Math.round(oy + f.y * scale) + 0.5, f.w * scale - 1, f.h * scale - 1);
      if (withLabels && f.w * scale > 48 && f.h * scale > 18) {
        c.fillText(f.name, ox + f.x * scale + 3, oy + f.y * scale + 12, f.w * scale - 6);
      }
    }
  }
}

function render(): void {
  const dpr = window.devicePixelRatio || 1;
  const { w, h } = viewport();
  if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) {
    canvas.width = Math.round(w * dpr);
    canvas.height = Math.round(h * dpr);
  }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.fillStyle = palette().bg;
  ctx.fillRect(0, 0, w, h);
  if (sheet) drawSheet(ctx, sheet, zoom, panX, panY, true);
  if (sheet) statusEl.textContent = `${sheet.width} × ${sheet.height} px · ${Math.round(zoom * 100)}%`;
}

let renderQueued = false;
function scheduleRender(): void {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => {
    renderQueued = false;
    render();
  });
}

// --- thumbnail -------------------------------------------------------------

function sendThumbnail(s: Sheet): void {
  if (thumbnailSent) return;
  thumbnailSent = true;
  const scale = Math.min(1, 256 / Math.max(s.width, s.height));
  const off = document.createElement("canvas");
  off.width = Math.max(1, Math.round(s.width * scale));
  off.height = Math.max(1, Math.round(s.height * scale));
  const c = off.getContext("2d");
  if (!c) return;
  drawSheet(c, s, scale, 0, 0, false);
  off.toBlob((blob) => {
    if (blob) provideThumbnail(blob).catch(() => {});
  }, "image/png");
}

// --- file intake -----------------------------------------------------------

function showFrames(frames: readonly AtlasFrame[], image: string): void {
  framesEl.hidden = frames.length === 0;
  framesTitle.textContent = image ? `Frames (${frames.length}) · ${image}` : `Frames (${frames.length})`;
  framesList.replaceChildren(
    ...frames.slice(0, 500).map((f) => {
      const li = document.createElement("li");
      li.textContent = `${f.name}  ${f.w}×${f.h}`;
      li.title = `${f.name} at ${f.x},${f.y} (${f.w}×${f.h})`;
      return li;
    }),
  );
}

async function decode(bytes: ArrayBuffer, file: PreviewFile): Promise<Sheet> {
  if (isAtlasFile(file.name, file.mimeType)) {
    const atlas = parseAtlas(new TextDecoder().decode(bytes));
    showFrames(atlas.frames, atlas.image);
    return { width: atlas.width, height: atlas.height, image: null, frames: atlas.frames };
  }
  let image: ImageBitmap;
  try {
    image = await createImageBitmap(new Blob([bytes], { type: file.mimeType || "application/octet-stream" }));
  } catch {
    throw new Error(`Could not decode ${file.name} as an image.`);
  }
  showFrames([], "");
  return { width: image.width, height: image.height, image, frames: [] };
}

function shortName(name: string, room: number): string {
  const chars = [...name];
  return chars.length <= room ? name : `${chars.slice(0, room - 1).join("")}…`;
}

onInit((init) => {
  theme = init.theme;
  document.documentElement.dataset.theme = theme;
  fileName = init.file.name;
  statusEl.textContent = `Loading ${fileName}…`;
  scheduleRender();
});

onFile((bytes, file) => {
  thumbnailSent = false;
  decode(bytes, file)
    .then((decoded) => {
      sheet = decoded;
      fit();
      render();
      const what = decoded.frames.length > 0 ? ` · ${decoded.frames.length} frames` : "";
      setTitle(`${shortName(file.name, 80)} — ${decoded.width}×${decoded.height}${what}`);
      publishToolbar();
      sendThumbnail(decoded);
    })
    .catch((err: unknown) => {
      sheet = null;
      const message = err instanceof Error ? err.message : String(err);
      statusEl.textContent = message;
      reportError(message);
      render();
      publishToolbar();
    });
});

function applyLayers(next: Partial<Layers>, share: boolean): void {
  let changed = false;
  for (const key of ["grid", "frames", "background"] as const) {
    const value = next[key];
    if (typeof value === "boolean" && layers[key] !== value) {
      layers[key] = value;
      changed = true;
    }
  }
  if (!changed) return;
  if (share) sendState({ ...layers });
  publishToolbar();
  scheduleRender();
}

onToolbar((id, value) => {
  const { w, h } = viewport();
  switch (id) {
    case "zoom":
      if (typeof value === "number") zoomAt(value, w / 2, h / 2);
      break;
    case "fit":
      fit();
      break;
    case "grid":
    case "frames":
    case "background":
      if (typeof value === "boolean") applyLayers({ [id]: value }, true);
      return;
    default:
      return;
  }
  publishToolbar();
  scheduleRender();
});

// Another member toggled a layer: apply it without echoing it back.
onState((state) => {
  if (typeof state === "object" && state !== null) applyLayers(state as Partial<Layers>, false);
});

onResize(() => scheduleRender());
window.addEventListener("resize", () => scheduleRender());

// --- pointer: drag to pan, wheel to zoom --------------------------------------

let drag: { x: number; y: number; id: number } | null = null;
canvas.addEventListener("pointerdown", (ev) => {
  drag = { x: ev.clientX, y: ev.clientY, id: ev.pointerId };
  canvas.setPointerCapture(ev.pointerId);
  canvas.classList.add("dragging");
});
canvas.addEventListener("pointermove", (ev) => {
  if (!drag || drag.id !== ev.pointerId) return;
  panX += ev.clientX - drag.x;
  panY += ev.clientY - drag.y;
  drag = { x: ev.clientX, y: ev.clientY, id: ev.pointerId };
  scheduleRender();
});
const endDrag = (ev: PointerEvent) => {
  if (drag?.id !== ev.pointerId) return;
  drag = null;
  canvas.classList.remove("dragging");
};
canvas.addEventListener("pointerup", endDrag);
canvas.addEventListener("pointercancel", endDrag);
canvas.addEventListener(
  "wheel",
  (ev) => {
    if (!sheet) return;
    ev.preventDefault();
    const rect = canvas.getBoundingClientRect();
    zoomAt(zoom * Math.exp(-ev.deltaY * 0.0015), ev.clientX - rect.left, ev.clientY - rect.top);
    publishToolbar();
    scheduleRender();
  },
  { passive: false },
);

// Initial toolbar (the declared one, with state) and background.
publishToolbar();
render();
