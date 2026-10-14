// The previewer frame protocol (bnbloc.previewer.contract/v1): the ONE
// definition of every message between the host (the bnbloc web client) and a
// previewer bundle running in a sandboxed opaque-origin iframe. Both sides
// import it — the host (web/src/lib/previewerFrame.ts) and the frame SDK
// (./index.ts) — so the shapes, limits and code-owned vocabularies cannot
// drift. The Go package validator (core/pkgmeta/previewer_validation.go)
// mirrors the vocabularies below and a Go test compares them to this file.
//
// Pure: no DOM globals, no dependencies, so it is unit-tested under node.
//
//   host  -> frame: preview.init      {protocol, file:{name,mimeType,sizeBytes,version}, theme, viewport}
//   host  -> frame: preview.file      {bytes: ArrayBuffer}            (transferred)
//   host  -> frame: preview.toolbar   {id, value}                     (a toolbar gesture)
//   host  -> frame: preview.state     {state, actor}                  (live state from the room)
//   host  -> frame: preview.resize    {w, h}
//   frame -> host : preview.ready
//   frame -> host : preview.toolbar.set {items}
//   frame -> host : preview.state.send  {state}                       (relayed as artifact_state)
//   frame -> host : preview.thumbnail   {png: ArrayBuffer}
//   frame -> host : preview.error       {message}
//   frame -> host : preview.title       {text}
//   frame -> host : preview.selection   {selection: {label, detail} | null}  (capability "selection")

/** Frame protocol version carried in preview.init. */
export const PREVIEWER_PROTOCOL = 1;

/** Upper bound on one serialized live-state value (the room stream carries it). */
export const STATE_LIMIT = 16 * 1024;
/** Upper bound on a previewer-made thumbnail (the thumbnail route's cap). */
export const THUMBNAIL_LIMIT = 512 * 1024;
/** Upper bound on toolbar items (declared and runtime). */
export const TOOLBAR_LIMIT = 20;
export const TOOLBAR_LABEL_LIMIT = 40;
export const TITLE_LIMIT = 120;
export const ERROR_LIMIT = 500;
/** A selection's chip label and the content it carries to the agent. */
export const SELECTION_LABEL_LIMIT = 120;
export const SELECTION_DETAIL_LIMIT = 2000;
/** Package suggestions: how many, and their label and prompt lengths. */
export const SUGGESTION_LIMIT = 6;
export const SUGGESTION_LABEL_LIMIT = 40;
export const SUGGESTION_PROMPT_LIMIT = 500;
/** Zoom values are scale factors (1 = 100%). */
export const ZOOM_MIN = 0.01;
export const ZOOM_MAX = 64;

/** Code-owned capability vocabulary (package `previewer.capabilities`). */
export const PREVIEWER_CAPABILITIES = ["thumbnail", "liveState", "toolbar", "selection"] as const;
export type PreviewerCapability = (typeof PREVIEWER_CAPABILITIES)[number];

/** Code-owned toolbar item kinds. */
export const TOOLBAR_KINDS = ["button", "toggle", "zoom"] as const;
export type ToolbarKind = (typeof TOOLBAR_KINDS)[number];

/** Code-owned toolbar icon names; the host maps each to its own icon set, so
 *  a previewer never ships images into the host's chrome. */
export const TOOLBAR_ICONS = [
  "zoom-in",
  "zoom-out",
  "fit",
  "expand",
  "grid",
  "layers",
  "background",
  "frames",
  "play",
  "pause",
  "reload",
  "eye",
  "ruler",
  "crop",
  "palette",
  "info",
] as const;
export type ToolbarIcon = (typeof TOOLBAR_ICONS)[number];

const TOOLBAR_ID_RE = /^[a-z][a-z0-9-]{0,31}$/;

/** One toolbar control. The package declares the initial set; the frame may
 *  replace it at runtime (preview.toolbar.set) under the same rules, adding
 *  state: `active` (toggle), `value` (zoom factor), `disabled`. */
export interface ToolbarItem {
  readonly id: string;
  readonly icon: ToolbarIcon;
  readonly label: string;
  readonly kind: ToolbarKind;
  readonly active?: boolean;
  readonly value?: number;
  readonly disabled?: boolean;
}

export type Theme = "light" | "dark";

/** What the member points at in the file: the chip's label ("Slide 4",
 *  "Revenue!B3:D5") and the content it names (the selected text, the
 *  range's values), which travels to the agent as the file's data. */
export interface Selection {
  readonly label: string;
  readonly detail: string;
}

export interface PreviewFile {
  readonly name: string;
  readonly mimeType: string;
  readonly sizeBytes: number;
  readonly version: number;
}

export interface Viewport {
  readonly w: number;
  readonly h: number;
}

export type HostMessage =
  | {
      readonly type: "preview.init";
      readonly protocol: number;
      readonly file: PreviewFile;
      readonly theme: Theme;
      readonly viewport: Viewport;
    }
  | { readonly type: "preview.file"; readonly bytes: ArrayBuffer }
  | { readonly type: "preview.toolbar"; readonly id: string; readonly value: boolean | number | null }
  | { readonly type: "preview.state"; readonly state: unknown; readonly actor: string }
  | { readonly type: "preview.resize"; readonly w: number; readonly h: number };

export type FrameMessage =
  | { readonly type: "preview.ready" }
  | { readonly type: "preview.toolbar.set"; readonly items: readonly ToolbarItem[] }
  | { readonly type: "preview.state.send"; readonly state: unknown }
  | { readonly type: "preview.thumbnail"; readonly png: ArrayBuffer }
  | { readonly type: "preview.error"; readonly message: string }
  | { readonly type: "preview.title"; readonly text: string }
  | { readonly type: "preview.selection"; readonly selection: Selection | null };

type Rec = Readonly<Record<string, unknown>>;

function isRecord(value: unknown): value is Rec {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isArrayBuffer(value: unknown): value is ArrayBuffer {
  return Object.prototype.toString.call(value) === "[object ArrayBuffer]";
}

function boundedText(value: unknown, limit: number, allowEmpty = false): string | null {
  if (typeof value !== "string") return null;
  if (!allowEmpty && value.trim() === "") return null;
  return [...value].length <= limit ? value : null;
}

function finiteNonNegative(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : null;
}

/** Serializes a live-state value, or null when it is not JSON or too large. */
export function stateKey(state: unknown): string | null {
  let key: string | undefined;
  try {
    key = JSON.stringify(state);
  } catch {
    return null;
  }
  if (typeof key !== "string" || new TextEncoder().encode(key).length > STATE_LIMIT) return null;
  return key;
}

const PNG_SIGNATURE = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];

/** True for a non-empty PNG within THUMBNAIL_LIMIT (signature checked). */
export function validThumbnail(png: ArrayBuffer): boolean {
  if (png.byteLength <= PNG_SIGNATURE.length || png.byteLength > THUMBNAIL_LIMIT) return false;
  const head = new Uint8Array(png, 0, PNG_SIGNATURE.length);
  return PNG_SIGNATURE.every((byte, i) => head[i] === byte);
}

/** Validates one toolbar item (declared or runtime). */
export function readToolbarItem(value: unknown): ToolbarItem | null {
  if (!isRecord(value)) return null;
  const { id, icon, label, kind, active, value: zoom, disabled } = value;
  if (typeof id !== "string" || !TOOLBAR_ID_RE.test(id)) return null;
  if (typeof icon !== "string" || !(TOOLBAR_ICONS as readonly string[]).includes(icon)) return null;
  if (typeof kind !== "string" || !(TOOLBAR_KINDS as readonly string[]).includes(kind)) return null;
  const text = boundedText(label, TOOLBAR_LABEL_LIMIT);
  if (text === null) return null;
  if (active !== undefined && typeof active !== "boolean") return null;
  if (disabled !== undefined && typeof disabled !== "boolean") return null;
  if (zoom !== undefined && (typeof zoom !== "number" || !Number.isFinite(zoom) || zoom < ZOOM_MIN || zoom > ZOOM_MAX)) return null;
  if (active !== undefined && kind !== "toggle") return null;
  if (zoom !== undefined && kind !== "zoom") return null;
  return {
    id,
    icon: icon as ToolbarIcon,
    label: text,
    kind: kind as ToolbarKind,
    ...(active !== undefined ? { active } : {}),
    ...(zoom !== undefined ? { value: zoom } : {}),
    ...(disabled !== undefined ? { disabled } : {}),
  };
}

/** Validates a toolbar list: at most TOOLBAR_LIMIT valid items, unique ids. */
export function readToolbar(value: unknown): readonly ToolbarItem[] | null {
  if (!Array.isArray(value) || value.length > TOOLBAR_LIMIT) return null;
  const items: ToolbarItem[] = [];
  const seen = new Set<string>();
  for (const raw of value) {
    const item = readToolbarItem(raw);
    if (!item || seen.has(item.id)) return null;
    seen.add(item.id);
    items.push(item);
  }
  return items;
}

/** The value a toolbar gesture carries for an item kind: null for a button,
 *  a boolean for a toggle, a zoom factor for a zoom control. */
export function validToolbarValue(kind: ToolbarKind, value: unknown): value is boolean | number | null {
  switch (kind) {
    case "button":
      return value === null;
    case "toggle":
      return typeof value === "boolean";
    case "zoom":
      return typeof value === "number" && Number.isFinite(value) && value >= ZOOM_MIN && value <= ZOOM_MAX;
  }
}

/** Validates the payload of a frame -> host message (the host additionally
 *  checks the sender: see web/src/lib/previewerFrame.ts). Returns null for
 *  anything unknown, malformed or over a limit. */
export function readFrameMessage(data: unknown): FrameMessage | null {
  if (!isRecord(data)) return null;
  switch (data.type) {
    case "preview.ready":
      return { type: "preview.ready" };
    case "preview.toolbar.set": {
      const items = readToolbar(data.items);
      return items ? { type: "preview.toolbar.set", items } : null;
    }
    case "preview.state.send":
      return stateKey(data.state) === null ? null : { type: "preview.state.send", state: data.state };
    case "preview.thumbnail":
      return isArrayBuffer(data.png) && validThumbnail(data.png) ? { type: "preview.thumbnail", png: data.png } : null;
    case "preview.error": {
      const message = boundedText(data.message, ERROR_LIMIT);
      return message === null ? null : { type: "preview.error", message };
    }
    case "preview.title": {
      const text = boundedText(data.text, TITLE_LIMIT, true);
      return text === null ? null : { type: "preview.title", text };
    }
    case "preview.selection": {
      if (data.selection === null) return { type: "preview.selection", selection: null };
      const sel = readSelection(data.selection);
      return sel ? { type: "preview.selection", selection: sel } : null;
    }
    default:
      return null;
  }
}

/** Validates a selection: a label within SELECTION_LABEL_LIMIT, a detail
 *  (possibly empty) within SELECTION_DETAIL_LIMIT. */
export function readSelection(value: unknown): Selection | null {
  if (!isRecord(value)) return null;
  const label = boundedText(value.label, SELECTION_LABEL_LIMIT);
  const detail = boundedText(value.detail ?? "", SELECTION_DETAIL_LIMIT, true);
  return label === null || detail === null ? null : { label, detail };
}

/** A package's starting prompt for the Ask AI panel (previewer.suggestions). */
export interface Suggestion {
  readonly label: string;
  readonly prompt: string;
}

/** Validates a package's suggestions list (absent = none). */
export function readSuggestions(value: unknown): readonly Suggestion[] | null {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > SUGGESTION_LIMIT) return null;
  const out: Suggestion[] = [];
  for (const item of value) {
    if (!isRecord(item)) return null;
    const label = boundedText(item.label, SUGGESTION_LABEL_LIMIT);
    const prompt = boundedText(item.prompt, SUGGESTION_PROMPT_LIMIT);
    if (label === null || prompt === null) return null;
    out.push({ label, prompt });
  }
  return out;
}

/** Fits a selection to the limits (the frame side: a long selection is cut,
 *  marked with an ellipsis, rather than refused by the host). */
export function fitSelection(label: string, detail: string): Selection {
  const cut = (s: string, n: number) => {
    const chars = [...s];
    return chars.length <= n ? s : `${chars.slice(0, n - 1).join("")}…`;
  };
  return { label: cut(label.trim() || "Selection", SELECTION_LABEL_LIMIT), detail: cut(detail, SELECTION_DETAIL_LIMIT) };
}

/** Validates the payload of a host -> frame message (the SDK additionally
 *  checks the sender is its parent window and the host origin). */
export function readHostMessage(data: unknown): HostMessage | null {
  if (!isRecord(data)) return null;
  switch (data.type) {
    case "preview.init": {
      const file = data.file;
      const viewport = data.viewport;
      if (data.protocol !== PREVIEWER_PROTOCOL || !isRecord(file) || !isRecord(viewport)) return null;
      if (typeof file.name !== "string" || typeof file.mimeType !== "string") return null;
      const sizeBytes = finiteNonNegative(file.sizeBytes);
      const version = finiteNonNegative(file.version);
      const w = finiteNonNegative(viewport.w);
      const h = finiteNonNegative(viewport.h);
      if (sizeBytes === null || version === null || w === null || h === null) return null;
      if (data.theme !== "light" && data.theme !== "dark") return null;
      return {
        type: "preview.init",
        protocol: PREVIEWER_PROTOCOL,
        file: { name: file.name, mimeType: file.mimeType, sizeBytes, version },
        theme: data.theme,
        viewport: { w, h },
      };
    }
    case "preview.file":
      return isArrayBuffer(data.bytes) ? { type: "preview.file", bytes: data.bytes } : null;
    case "preview.toolbar": {
      if (typeof data.id !== "string" || !TOOLBAR_ID_RE.test(data.id)) return null;
      const value = data.value;
      if (value !== null && typeof value !== "boolean" && (typeof value !== "number" || !Number.isFinite(value))) return null;
      return { type: "preview.toolbar", id: data.id, value };
    }
    case "preview.state":
      if (typeof data.actor !== "string" || stateKey(data.state) === null) return null;
      return { type: "preview.state", state: data.state, actor: data.actor };
    case "preview.resize": {
      const w = finiteNonNegative(data.w);
      const h = finiteNonNegative(data.h);
      return w === null || h === null ? null : { type: "preview.resize", w, h };
    }
    default:
      return null;
  }
}

/** Parses the host origin the frame URL carries in its fragment
 *  (`#host=<origin>`): the only origin the frame accepts messages from. */
export function hostOriginFromHash(hash: string): string {
  try {
    return new URLSearchParams(hash.replace(/^#/, "")).get("host") ?? "";
  } catch {
    return "";
  }
}
