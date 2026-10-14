import type React from "react";
import { useSyncExternalStore } from "react";

// The ONE artifact viewer registry: built-in viewers (builtin.ts) and
// installed previewer components (registered at runtime from the member
// catalog) share it, so selection, "Open with…" and the tab toolbar treat
// both the same way. Pure module (type-only React import) so selection is
// unit-tested under node.
//
// Selection (selectViewers): every viewer whose match claims the file is a
// candidate, ordered by
//   1. specificity: exact MIME (4) > extension (3) > legacy kind (2) >
//      MIME wildcard "type/*" (1);
//   2. rank, ascending (lower wins; built-ins sit at 10, so a component that
//      should beat a built-in of the same specificity declares < 10);
//   3. an installed component before a built-in, then id.
// A viewer with maxBytes drops out for larger files. selectViewer picks the
// member's explicit choice when it is still a candidate, else the best.

/** What a viewer is told about the file it renders. */
export interface ArtifactFileInfo {
  /** Display name with extension ("report.pdf"). */
  readonly name: string;
  /** Effective MIME type ("" when unknown). */
  readonly mimeType: string;
  /** Lower-case extension without the dot ("" when none). */
  readonly extension: string;
  /** Legacy artifact kind (markdown | code | image | html | app | file …). */
  readonly kind: string;
  readonly language?: string;
  readonly sizeBytes?: number;
  readonly version: number;
}

/** A toolbar control a viewer contributes to its tab's toolbar. Structurally
 *  the previewer frame protocol's ToolbarItem (web/previewer-sdk) — id, icon
 *  name, label, kind, and state: `active` (toggle), `value` (zoom factor,
 *  1 = 100%), `disabled` — plus a built-in-only `select` kind (`options`,
 *  `selected`) for short preset lists such as device widths. */
export interface ViewerToolbarItem {
  readonly id: string;
  /** Host icon name (see ViewerToolbar's icon map); unknown names render the
   *  label as text. */
  readonly icon: string;
  readonly label: string;
  readonly kind: "button" | "toggle" | "zoom" | "select";
  readonly active?: boolean;
  readonly value?: number;
  readonly disabled?: boolean;
  readonly options?: readonly { readonly value: string; readonly label: string }[];
  readonly selected?: string;
}

/** A toolbar gesture's value: null (button), boolean (toggle), zoom factor
 *  (zoom), option value (select). */
export type ViewerToolbarValue = null | boolean | number | string;

/** The tab's toolbar as seen by its viewer: publish items, receive
 *  gestures. A remote previewer's host component forwards
 *  preview.toolbar.set to set() and gestures to the frame. */
export interface ViewerToolbarSink {
  set(items: readonly ViewerToolbarItem[]): void;
  subscribe(handler: (id: string, value: ViewerToolbarValue) => void): () => void;
}

/** Where a `site` version's files are served from. The host supplies it:
 *  the workbench mints a member site ticket, the public share page hands
 *  the link's own site URL. Called again whenever the viewer needs a fresh
 *  URL (load, reload, open in new tab, before a ticket expires). */
export interface SiteSource {
  resolve(): Promise<{ readonly siteUrl: string; readonly expiresAtMs?: number }>;
}

export interface ArtifactViewerProps {
  /** The owning room; "" on the public share page (no room context: a
   *  viewer must not call room APIs then). */
  readonly roomId: string;
  readonly actor?: string;
  /** "" for a file that is not an artifact (a chat attachment): no live
   *  state, no stored thumbnail, no artifact routes. */
  readonly artifactId: string;
  readonly version: number;
  readonly file: ArtifactFileInfo;
  /** The version's bytes, fetched by the host (workbench: the member content
   *  route; public share page: the public content route). Empty for
   *  `input: "site"` viewers, which load the site through `site`. */
  readonly blob: Blob;
  /** The site's frame URL source; set only for `input: "site"` viewers. */
  readonly site: SiteSource | null;
  /** UTF-8 text of the bytes for `input: "text"` viewers; "" otherwise. */
  readonly content: string;
  readonly toolbar: ViewerToolbarSink;
  readonly onLocalState?: (s: unknown) => void;
  readonly remoteState?: { readonly state: unknown; readonly actor: string; readonly nonce: number };
}

export interface ViewerMatch {
  /** Exact ("image/png") or wildcard ("image/*") MIME types, lower-case. */
  readonly mimeTypes?: readonly string[];
  /** Extensions, lower-case, with or without the leading dot. */
  readonly extensions?: readonly string[];
  /** Legacy artifact kinds (inline artifacts carry no MIME type). */
  readonly kinds?: readonly string[];
}

export interface ArtifactViewer {
  readonly id: string;
  readonly label: string;
  readonly match: ViewerMatch;
  /** Lower wins among equally specific matches; built-ins use 10. */
  readonly rank: number;
  /** "text": the host decodes the bytes into `content`; "blob": bytes only;
   *  "site": no bytes — the viewer frames the site through `site`. */
  readonly input: "text" | "blob" | "site";
  /** How the viewer contains untrusted content (documentation + review). */
  readonly safety: "safe" | "sanitized" | "sandboxed";
  readonly source: "builtin" | "component";
  /** Files larger than this fall through to the next candidate. */
  readonly maxBytes?: number;
  readonly load: () => Promise<{ default: React.ComponentType<ArtifactViewerProps> }>;
}

export interface ViewerCandidate {
  readonly viewer: ArtifactViewer;
  readonly specificity: number;
}

const viewers: ArtifactViewer[] = [];
const listeners = new Set<() => void>();
let revision = 0;

function changed(): void {
  revision += 1;
  for (const listener of listeners) listener();
}

/** Registers (or replaces, by id) a viewer; returns its unregister. */
export function registerArtifactViewer(viewer: ArtifactViewer): () => void {
  const existing = viewers.findIndex((candidate) => candidate.id === viewer.id);
  if (existing >= 0) viewers[existing] = viewer;
  else viewers.push(viewer);
  changed();
  return () => unregisterArtifactViewer(viewer.id);
}

export function unregisterArtifactViewer(id: string): void {
  const at = viewers.findIndex((candidate) => candidate.id === id);
  if (at < 0) return;
  viewers.splice(at, 1);
  changed();
}

/** Subscribes to registry changes (runtime installs/uninstalls), for
 *  useSyncExternalStore with registryRevision as the snapshot. */
export function subscribeArtifactViewers(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function registryRevision(): number {
  return revision;
}

export function listArtifactViewers(): readonly ArtifactViewer[] {
  return [...viewers];
}

function normalizeExt(ext: string): string {
  return ext.replace(/^\./, "").toLowerCase();
}

/** How specifically a viewer claims a file; 0 = not at all. A multi-part
 *  extension (".atlas.json") matching the end of the name is the most
 *  specific claim (it beats an exact generic MIME such as application/json),
 *  then exact MIME, plain extension, kind, MIME wildcard. */
export function matchSpecificity(match: ViewerMatch, file: Pick<ArtifactFileInfo, "mimeType" | "extension" | "kind" | "name">): number {
  const mime = file.mimeType.toLowerCase();
  const mimes = (match.mimeTypes ?? []).map((m) => m.toLowerCase());
  if (mime && mimes.includes(mime)) return 4;
  const name = file.name.toLowerCase();
  const ext = file.extension.toLowerCase();
  for (const raw of match.extensions ?? []) {
    const claimed = normalizeExt(raw);
    if (!claimed) continue;
    if (claimed.includes(".") && name.endsWith(`.${claimed}`)) return 5;
  }
  for (const raw of match.extensions ?? []) {
    if (normalizeExt(raw) === ext) return 3;
  }
  if (file.kind && (match.kinds ?? []).some((k) => k.toLowerCase() === file.kind.toLowerCase())) return 2;
  const top = mime.split("/")[0];
  if (top && mimes.includes(`${top}/*`)) return 1;
  return 0;
}

/** Every viewer that can render the file, best first. */
export function selectViewers(
  file: ArtifactFileInfo,
  from: readonly ArtifactViewer[] = viewers,
): ViewerCandidate[] {
  const out: ViewerCandidate[] = [];
  for (const viewer of from) {
    const specificity = matchSpecificity(viewer.match, file);
    if (specificity === 0) continue;
    if (viewer.maxBytes !== undefined && file.sizeBytes !== undefined && file.sizeBytes > viewer.maxBytes) continue;
    out.push({ viewer, specificity });
  }
  return out.sort(
    (a, b) =>
      b.specificity - a.specificity ||
      a.viewer.rank - b.viewer.rank ||
      (a.viewer.source === b.viewer.source ? 0 : a.viewer.source === "component" ? -1 : 1) ||
      a.viewer.id.localeCompare(b.viewer.id),
  );
}

/** The viewer to use: the preferred one while it still claims the file,
 *  else the best candidate; null = no viewer (download card). */
export function selectViewer(
  file: ArtifactFileInfo,
  preferredId?: string,
  from: readonly ArtifactViewer[] = viewers,
): ArtifactViewer | null {
  const candidates = selectViewers(file, from);
  if (preferredId) {
    const preferred = candidates.find((c) => c.viewer.id === preferredId);
    if (preferred) return preferred.viewer;
  }
  return candidates[0]?.viewer ?? null;
}

/** Re-renders on registry changes (runtime installs). */
export function useViewerRegistry(): number {
  return useSyncExternalStore(subscribeArtifactViewers, registryRevision, registryRevision);
}
