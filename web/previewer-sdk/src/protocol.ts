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
//   host  -> frame: preview.init      {protocol, file:{name,mimeType,sizeBytes,version}, theme, viewport, viewOnly?}
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
//   host  -> frame: preview.units.request {id, file, bytes}                  (capability "units")
//   frame -> host : preview.units       {id, units, truncated} | {id, error}
//   host  -> frame: preview.reveal      {at}                                  (capability "units")
//   frame -> host : preview.save        {bytes, summary?}                     (capability "edit")
//   host  -> frame: preview.saved       {version}                             (answer to preview.save)
//   host  -> frame: preview.save.failed {message}                             (answer to preview.save)
//   frame -> host : preview.draft       {prompt}                              (capability "draft")
//   host  -> frame: preview.drafted     {reply}                               (answer to preview.draft)
//   host  -> frame: preview.draft.failed {message}                            (answer to preview.draft)

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
/** Capability "edit": one saved file, and the version's summary line. The
 *  default room version cap (services/room/artifact_files.go,
 *  DefaultArtifactMaxBytes); an operator may lower BNBLOC_ARTIFACT_MAX_BYTES,
 *  and the host refuses a larger save with its own message. */
export const SAVE_LIMIT = 100 << 20;
export const SAVE_SUMMARY_LIMIT = 200;
/** Capability "draft": one sentence the host sends to the assist task the
 *  package declares (previewer.draft.task); the reply is that task's JSON
 *  fields, bounded by its serialized size. */
export const DRAFT_PROMPT_LIMIT = 1000;
export const DRAFT_REPLY_LIMIT = 64 * 1024;
/** A selection's chip label and the content it carries to the agent. */
export const SELECTION_LABEL_LIMIT = 120;
export const SELECTION_DETAIL_LIMIT = 2000;
/** The unit keys a selection covers (Compare units): how many, how long. */
export const SELECTION_KEYS_LIMIT = 500;
export const SELECTION_KEY_LIMIT = 200;

/** Code-owned selection kinds (what a member points at); prompt packs scope
 *  selection suggestions by them. */
export const SELECTION_KINDS = ["text", "block", "slide", "cells", "region", "time"] as const;
export type SelectionKind = (typeof SELECTION_KINDS)[number];
/** Units (capability "units", Compare): how many, each one's key, label
 *  and text lengths, and the whole list's serialized size. */
export const UNITS_LIMIT = 5000;
export const UNIT_KEY_LIMIT = 200;
export const UNIT_LABEL_LIMIT = 120;
export const UNIT_TEXT_LIMIT = 4000;
export const UNITS_BYTES_LIMIT = 2 * 1024 * 1024;
/** Zoom values are scale factors (1 = 100%). */
export const ZOOM_MIN = 0.01;
export const ZOOM_MAX = 64;

/** Code-owned capability vocabulary (package `previewer.capabilities`). */
// "edit": the frame may save its scene as the next version of the file
// (preview.save); the host publishes it on the version the frame was handed.
// "draft": the frame may ask for a draft from a sentence (preview.draft); the
// host runs the assist task the package declares, and no other.
export const PREVIEWER_CAPABILITIES = ["thumbnail", "liveState", "toolbar", "selection", "units", "edit", "draft"] as const;
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
  /** What was selected (drives selection suggestions). */
  readonly kind?: SelectionKind;
  /** Where it shows, in the viewer's box (CSS pixels): the host places its
   *  selection action there. */
  readonly rect?: Rect;
  /** The Compare unit keys it covers (a slide's, every cell of a range). */
  readonly keys?: readonly string[];
}

export interface Rect {
  readonly x: number;
  readonly y: number;
  readonly w: number;
  readonly h: number;
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
      /** The frame is shown to someone who only looks (a shared link, an older
       *  version): it should offer no editing. The host refuses what such a
       *  frame asks for anyway (previewerFrame.viewOnlyRefusal). */
      readonly viewOnly?: true;
    }
  | { readonly type: "preview.file"; readonly bytes: ArrayBuffer }
  | { readonly type: "preview.toolbar"; readonly id: string; readonly value: boolean | number | null }
  | { readonly type: "preview.state"; readonly state: unknown; readonly actor: string }
  | { readonly type: "preview.resize"; readonly w: number; readonly h: number }
  | { readonly type: "preview.units.request"; readonly id: string; readonly file: PreviewFile; readonly bytes: ArrayBuffer }
  | { readonly type: "preview.reveal"; readonly at: string }
  | { readonly type: "preview.saved"; readonly version: number }
  | { readonly type: "preview.save.failed"; readonly message: string }
  | { readonly type: "preview.drafted"; readonly reply: Rec }
  | { readonly type: "preview.draft.failed"; readonly message: string };

/** One unit of a version for Compare (docs/prd/2026-10-14-file-prompts-and-compare.md
 *  rule 7): a slide, a paragraph, a cell, a page. key is a stable identity
 *  across versions when the format has one (a slide id, a cell address). */
export interface Unit {
  readonly key?: string;
  readonly label: string;
  readonly text: string;
  /** Where it is in this version ("slide:2", "Revenue!C3", "p:7"): the
   *  host hands it back in preview.reveal to show the unit. */
  readonly at?: string;
}

export type FrameMessage =
  | { readonly type: "preview.ready" }
  | { readonly type: "preview.toolbar.set"; readonly items: readonly ToolbarItem[] }
  | { readonly type: "preview.state.send"; readonly state: unknown }
  | { readonly type: "preview.thumbnail"; readonly png: ArrayBuffer }
  | { readonly type: "preview.error"; readonly message: string }
  | { readonly type: "preview.title"; readonly text: string }
  | { readonly type: "preview.selection"; readonly selection: Selection | null }
  | { readonly type: "preview.units"; readonly id: string; readonly units: readonly Unit[]; readonly truncated: boolean }
  | { readonly type: "preview.units"; readonly id: string; readonly error: string }
  | { readonly type: "preview.save"; readonly bytes: ArrayBuffer; readonly summary?: string }
  | { readonly type: "preview.draft"; readonly prompt: string };

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

/** True when the value serializes to JSON within limit bytes. */
function jsonWithin(value: unknown, limit: number): boolean {
  let json: string | undefined;
  try {
    json = JSON.stringify(value);
  } catch {
    return false;
  }
  return typeof json === "string" && new TextEncoder().encode(json).length <= limit;
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
    case "preview.units": {
      if (typeof data.id !== "string" || !UNITS_ID_RE.test(data.id)) return null;
      if (data.error !== undefined) {
        const error = boundedText(data.error, ERROR_LIMIT);
        return error === null ? null : { type: "preview.units", id: data.id, error };
      }
      const units = readUnits(data.units);
      return units ? { type: "preview.units", id: data.id, units, truncated: data.truncated === true } : null;
    }
    case "preview.draft": {
      const prompt = boundedText(data.prompt, DRAFT_PROMPT_LIMIT);
      return prompt === null ? null : { type: "preview.draft", prompt: prompt.trim() };
    }
    case "preview.save": {
      if (!isArrayBuffer(data.bytes) || data.bytes.byteLength === 0 || data.bytes.byteLength > SAVE_LIMIT) return null;
      if (data.summary === undefined) return { type: "preview.save", bytes: data.bytes };
      const summary = boundedText(data.summary, SAVE_SUMMARY_LIMIT, true);
      return summary === null ? null : { type: "preview.save", bytes: data.bytes, summary };
    }
    default:
      return null;
  }
}

const UNITS_ID_RE = /^[A-Za-z0-9_-]{1,64}$/;

/** Validates a unit list against the UNIT* limits (the host side). */
export function readUnits(value: unknown): readonly Unit[] | null {
  if (!Array.isArray(value) || value.length > UNITS_LIMIT) return null;
  const out: Unit[] = [];
  for (const item of value) {
    if (!isRecord(item)) return null;
    const label = boundedText(item.label, UNIT_LABEL_LIMIT, true);
    const text = boundedText(item.text, UNIT_TEXT_LIMIT, true);
    const key = item.key === undefined ? undefined : boundedText(item.key, UNIT_KEY_LIMIT);
    const at = item.at === undefined ? undefined : boundedText(item.at, UNIT_KEY_LIMIT);
    if (label === null || text === null || key === null || at === null) return null;
    out.push({ ...(key === undefined ? {} : { key }), label, text, ...(at === undefined ? {} : { at }) });
  }
  return JSON.stringify(out).length <= UNITS_BYTES_LIMIT ? out : null;
}

/** Fits units to the limits (the frame side): long texts and labels are cut
 *  with an ellipsis, and the list stops (truncated) at UNITS_LIMIT units or
 *  UNITS_BYTES_LIMIT, rather than being refused by the host. */
export function fitUnits(units: readonly { readonly key?: string; readonly label: string; readonly text: string; readonly at?: string }[]): { units: Unit[]; truncated: boolean } {
  const cut = (s: string, n: number) => {
    const chars = [...s];
    return chars.length <= n ? s : `${chars.slice(0, n - 1).join("")}…`;
  };
  const out: Unit[] = [];
  let bytes = 2;
  for (const u of units) {
    if (out.length >= UNITS_LIMIT) return { units: out, truncated: true };
    const key = u.key ? [...u.key].slice(0, UNIT_KEY_LIMIT).join("") : undefined;
    const at = u.at && [...u.at].length <= UNIT_KEY_LIMIT ? u.at : undefined;
    const unit: Unit = { ...(key ? { key } : {}), label: cut(u.label, UNIT_LABEL_LIMIT), text: cut(u.text, UNIT_TEXT_LIMIT), ...(at ? { at } : {}) };
    bytes += JSON.stringify(unit).length + 1;
    if (bytes > UNITS_BYTES_LIMIT) return { units: out, truncated: true };
    out.push(unit);
  }
  return { units: out, truncated: false };
}

/** Validates a selection: a label within SELECTION_LABEL_LIMIT, a detail
 *  (possibly empty) within SELECTION_DETAIL_LIMIT. */
export function readSelection(value: unknown): Selection | null {
  if (!isRecord(value)) return null;
  const label = boundedText(value.label, SELECTION_LABEL_LIMIT);
  const detail = boundedText(value.detail ?? "", SELECTION_DETAIL_LIMIT, true);
  if (label === null || detail === null) return null;
  let out: Selection = { label, detail };
  if (value.kind !== undefined) {
    if (!(SELECTION_KINDS as readonly unknown[]).includes(value.kind)) return null;
    out = { ...out, kind: value.kind as SelectionKind };
  }
  if (value.rect !== undefined) {
    const r = readRect(value.rect);
    if (!r) return null;
    out = { ...out, rect: r };
  }
  if (value.keys !== undefined) {
    if (!Array.isArray(value.keys) || value.keys.length > SELECTION_KEYS_LIMIT) return null;
    const keys = value.keys.map((k) => boundedText(k, SELECTION_KEY_LIMIT));
    if (keys.some((k) => k === null)) return null;
    out = { ...out, keys: keys as string[] };
  }
  return out;
}

function readRect(value: unknown): Rect | null {
  if (!isRecord(value)) return null;
  const n = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && Math.abs(v) <= 1e6 ? v : null);
  const x = n(value.x);
  const y = n(value.y);
  const w = n(value.w);
  const h = n(value.h);
  return x === null || y === null || w === null || h === null || w < 0 || h < 0 ? null : { x, y, w, h };
}

/** Fits a selection to the limits (the frame side: a long selection is cut,
 *  marked with an ellipsis, rather than refused by the host). */
export function fitSelection(draft: {
  readonly label: string;
  readonly detail?: string;
  readonly kind?: SelectionKind;
  readonly rect?: Rect;
  readonly keys?: readonly string[];
}): Selection {
  const cut = (s: string, n: number) => {
    const chars = [...s];
    return chars.length <= n ? s : `${chars.slice(0, n - 1).join("")}…`;
  };
  const label = cut(String(draft.label ?? "").trim() || "Selection", SELECTION_LABEL_LIMIT);
  const out: Selection = { label, detail: cut(String(draft.detail ?? ""), SELECTION_DETAIL_LIMIT) };
  const rect = draft.rect ? readRect(draft.rect) : null;
  // A selection covering more units than the limit says none (the host
  // falls back to comparing text), rather than a misleading part.
  const keys = draft.keys && draft.keys.length <= SELECTION_KEYS_LIMIT ? draft.keys.filter((k) => k && [...k].length <= SELECTION_KEY_LIMIT) : undefined;
  return {
    ...out,
    ...(draft.kind && (SELECTION_KINDS as readonly string[]).includes(draft.kind) ? { kind: draft.kind } : {}),
    ...(rect ? { rect } : {}),
    ...(keys && keys.length ? { keys } : {}),
  };
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
      // Absent means false; present, it must be a boolean.
      if (data.viewOnly !== undefined && typeof data.viewOnly !== "boolean") return null;
      return {
        type: "preview.init",
        protocol: PREVIEWER_PROTOCOL,
        file: { name: file.name, mimeType: file.mimeType, sizeBytes, version },
        theme: data.theme,
        viewport: { w, h },
        ...(data.viewOnly === true ? { viewOnly: true as const } : {}),
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
    case "preview.units.request": {
      const file = data.file;
      if (typeof data.id !== "string" || !UNITS_ID_RE.test(data.id) || !isRecord(file) || !isArrayBuffer(data.bytes)) return null;
      if (typeof file.name !== "string" || typeof file.mimeType !== "string") return null;
      const sizeBytes = finiteNonNegative(file.sizeBytes);
      const version = finiteNonNegative(file.version);
      if (sizeBytes === null || version === null) return null;
      return { type: "preview.units.request", id: data.id, file: { name: file.name, mimeType: file.mimeType, sizeBytes, version }, bytes: data.bytes };
    }
    case "preview.reveal": {
      const at = boundedText(data.at, UNIT_KEY_LIMIT);
      return at === null ? null : { type: "preview.reveal", at };
    }
    case "preview.saved": {
      const version = data.version;
      return typeof version === "number" && Number.isSafeInteger(version) && version > 0 ? { type: "preview.saved", version } : null;
    }
    case "preview.save.failed": {
      const message = boundedText(data.message, ERROR_LIMIT);
      return message === null ? null : { type: "preview.save.failed", message };
    }
    case "preview.drafted": {
      return isRecord(data.reply) && jsonWithin(data.reply, DRAFT_REPLY_LIMIT) ? { type: "preview.drafted", reply: data.reply } : null;
    }
    case "preview.draft.failed": {
      const message = boundedText(data.message, ERROR_LIMIT);
      return message === null ? null : { type: "preview.draft.failed", message };
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
