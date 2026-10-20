import type React from "react";
import type { FileFacts } from "../../lib/fileMatch.ts";
import type { Selection } from "../../../previewer-sdk/src/protocol.ts";
import { createFileRegistry, useRegistryRevision, type FileClaimant } from "../../lib/fileRegistry.ts";

// The ONE artifact viewer registry: built-in viewers (builtin.ts) and
// installed previewer components (registered at runtime from the member
// catalog) share it, so selection, "Open with…" and the tab toolbar treat
// both the same way. Pure module (type-only React import) so selection is
// unit-tested under node.
//
// Selection (selectViewers) is the generic file registry's
// (lib/fileRegistry.ts): specificity, rank, component before built-in.

/** What a viewer is told about the file it renders. */
export interface ArtifactFileInfo extends FileFacts {
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
  /** The part of the file the member points at (a slide, a cell range,
   *  selected text), null when none: the Ask AI panel names it. */
  readonly onSelection?: (s: ViewerSelection | null) => void;
  /** A unit to show (its Compare `at`); a new nonce shows it again. */
  readonly reveal?: { readonly at: string; readonly nonce: number };
  /** Publishes an exact edit the viewer made (a crop) as the file's next
   *  version, on the shown one; absent where the file cannot be versioned
   *  (a share page, an attachment, an older version). Resolves to the new
   *  version. */
  readonly onPublish?: (bytes: Blob, summary: string) => Promise<number>;
  /** Takes a cover the viewer drew of the version (a PNG: a PDF's first
   *  page, a previewer's thumbnail; `source` ranks it in a room, an
   *  installed previewer's above the app's own viewers'). The host decides
   *  where it goes: a room version's thumbnail, a work version's stored
   *  cover; absent where none is kept (a share page, an attachment). A
   *  viewer sends at most one. */
  readonly onCover?: (png: ArrayBuffer, source: "builtin" | "previewer") => void;
  /** Opens the room file of this name in the host's own tab (a live page's
   *  relative link); absent where there is no room tab to open (a share
   *  page, a pop-out, a cover job). */
  readonly onOpenFile?: (name: string) => void;
}

/** What a viewer reports as selected: the frame protocol's Selection
 *  (a label, the content it covers, and optionally its kind, place and unit
 *  keys), whether a previewer or a built-in viewer reports it. */
export type ViewerSelection = Selection & {
  /** A painted area's exact shape (a built-in viewer's brush): a PNG the
   *  image's size, transparent where it may change, made when the request
   *  is sent (docs/prd/2026-10-17-image-generation-redesign.md §2.7). */
  readonly mask?: () => Promise<Blob>;
};


export interface ArtifactViewer extends FileClaimant {
  readonly label: string;
  /** "text": the host decodes the bytes into `content`; "blob": bytes only;
   *  "site": no bytes — the viewer frames the site through `site`. */
  readonly input: "text" | "blob" | "site";
  /** How the viewer contains untrusted content (documentation + review). */
  readonly safety: "safe" | "sanitized" | "sandboxed";
  readonly load: () => Promise<{ default: React.ComponentType<ArtifactViewerProps> }>;
  /** How a card for this viewer's files gets a picture when no cover is
   *  stored. "text": the host draws the first lines as a page (for files this
   *  viewer shows as text; the file is read, never rendered). "render": the
   *  viewer sends a cover when it renders the file, so a card in view may have
   *  it rendered once, out of sight (lib/coverJobs). Absent: only a stored
   *  cover or an image. */
  readonly cover?: "text" | "render";
}

export interface ViewerCandidate {
  readonly viewer: ArtifactViewer;
  readonly specificity: number;
}

const viewers = createFileRegistry<ArtifactViewer>();

export const registerArtifactViewer = viewers.register;
export const unregisterArtifactViewer = viewers.unregister;
export const listArtifactViewers = viewers.list;

/** Every viewer that can render the file, best first. */
export function selectViewers(file: ArtifactFileInfo, from?: readonly ArtifactViewer[]): ViewerCandidate[] {
  return viewers.candidates(file, from).map((c) => ({ viewer: c.item, specificity: c.specificity }));
}

/** The viewer to use: the preferred one while it still claims the file,
 *  else the best candidate; null = no viewer (download card). */
export function selectViewer(file: ArtifactFileInfo, preferredId?: string, from?: readonly ArtifactViewer[]): ArtifactViewer | null {
  return viewers.select(file, preferredId, from);
}

/** Re-renders on registry changes (runtime installs). */
export function useViewerRegistry(): number {
  return useRegistryRevision(viewers);
}
