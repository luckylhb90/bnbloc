// Host side of the previewer frame protocol (bnbloc.previewer.contract/v1):
// catalog parsing, frame URLs, and inbound message validation. Pure (no React,
// no DOM globals) so it is unit-tested under node; RemotePreviewer wires it to
// a sandboxed iframe. The message shapes, limits and vocabularies are the
// SDK's protocol module — the single definition both sides import.
//
// Where a previewer bundle is loaded from:
//   - desktop: the shell's `bnbloc-previewer` scheme
//     (apps/desktop/src-tauri/src/previewer_scheme.rs), which serves the
//     sha256-verified bundle from the desktop's own component cache;
//   - browser: the gateway frame route GET /v1/previewers/{name}/{version}/
//     {ticket}/{path…} (services/gateway/previewers.go); the catalog hands
//     out the ticketed base as `frameUrl`.
// Either way the iframe is sandbox="allow-scripts" WITHOUT allow-same-origin
// (opaque origin), the document's CSP refuses all network, and the fragment
// `#host=<origin>` names the only origin the SDK accepts messages from.

import {
  DRAFT_REPLY_LIMIT,
  ERROR_LIMIT,
  PREVIEWER_CAPABILITIES,
  readFrameMessage,
  readToolbar,
  type FrameMessage,
  type HostMessage,
  type PreviewerCapability,
  type ToolbarItem,
} from "../../previewer-sdk/src/protocol.ts";
import { ApiError, errorText } from "./errors.ts";
import { schemeUrl, type ConvertFileSrc } from "./schemeUrl.ts";

/** Desktop URI scheme serving verified previewer bundles. */
export const PREVIEWER_SCHEME = "bnbloc-previewer";

/** A frame ticket is refreshed (catalog re-read) this long before expiry. */
export const FRAME_TICKET_SLACK_MS = 5 * 60 * 1000;

/** An installed previewer as the member catalog describes it. */
export interface PreviewerDescriptor {
  readonly name: string;
  readonly version: string;
  readonly description: string;
  readonly entry: string;
  readonly mimeTypes: readonly string[];
  readonly extensions: readonly string[];
  readonly rank: number;
  readonly capabilities: readonly PreviewerCapability[];
  readonly toolbar: readonly ToolbarItem[];
  readonly bundleSha256: string;
  /** Gateway-relative browser frame base (ticket included), ends with "/". */
  readonly frameUrl: string;
  readonly frameExpiresAtMs: number;
  /** The assist task the host runs for a frame's draft request (the package's
   *  draft.task); present only for a previewer that declares "draft". */
  readonly draftTask?: string;
}

type Rec = Readonly<Record<string, unknown>>;

function isRecord(value: unknown): value is Rec {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringList(value: unknown): readonly string[] | null {
  if (value === undefined) return [];
  return Array.isArray(value) && value.every((v) => typeof v === "string") ? value : null;
}

const NAME_RE = /^[a-z0-9][a-z0-9._-]*$/;
const SEGMENT_RE = /^[A-Za-z0-9._-]+$/;
const FRAME_URL_RE = /^\/v1\/previewers\/[^/?#]+\/[^/?#]+\/[^/?#]+\/$/;

/** True for a bundle-relative path the frame routes serve. */
export function validBundlePath(path: string): boolean {
  if (!path || path.length > 200) return false;
  return path.split("/").every((seg) => seg !== "." && seg !== ".." && SEGMENT_RE.test(seg));
}

/** Parses one member-catalog entry into a descriptor, or null when it is not
 *  an installed previewer with a well-formed block. The gateway validated the
 *  package at install; this keeps a malformed entry from reaching the
 *  registry or the frame URL. */
export function parsePreviewerEntry(entry: unknown): PreviewerDescriptor | null {
  if (!isRecord(entry) || entry.kind !== "previewer" || entry.installed !== true) return null;
  const block = entry.previewer;
  if (!isRecord(block) || !isRecord(block.match)) return null;
  const { name, version } = entry;
  if (typeof name !== "string" || !NAME_RE.test(name) || typeof version !== "string" || !version) return null;
  const mimeTypes = stringList(block.match.mimeTypes);
  const extensions = stringList(block.match.extensions);
  const caps = stringList(block.capabilities);
  const toolbar = readToolbar(block.toolbar ?? []);
  if (!mimeTypes || !extensions || !caps || !toolbar || mimeTypes.length + extensions.length === 0) return null;
  const { entry: entryPath, rank, frameUrl, frameExpiresAtMs, bundleSha256 } = block;
  if (typeof entryPath !== "string" || !validBundlePath(entryPath)) return null;
  if (typeof rank !== "number" || !Number.isInteger(rank) || rank < 1 || rank > 1000) return null;
  if (typeof frameUrl !== "string" || !FRAME_URL_RE.test(frameUrl)) return null;
  if (typeof frameExpiresAtMs !== "number" || typeof bundleSha256 !== "string") return null;
  const draft = block.draft;
  const draftTask = isRecord(draft) && typeof draft.task === "string" && draft.task !== "" ? draft.task : undefined;
  return {
    name,
    version,
    description: typeof entry.description === "string" ? entry.description : "",
    entry: entryPath,
    mimeTypes,
    extensions,
    rank,
    // The gateway refuses unknown capabilities at publish; one this host
    // predates is ignored, not a reason to drop the previewer.
    capabilities: caps.filter((c): c is PreviewerCapability => (PREVIEWER_CAPABILITIES as readonly string[]).includes(c)),
    toolbar,
    bundleSha256,
    frameUrl,
    frameExpiresAtMs,
    ...(draftTask === undefined ? {} : { draftTask }),
  };
}

/** Parses the member catalog response (GET /v1/components?kind=previewer). */
export function parsePreviewerCatalog(body: unknown): PreviewerDescriptor[] {
  if (!isRecord(body) || !Array.isArray(body.components)) return [];
  return body.components.map(parsePreviewerEntry).filter((d): d is PreviewerDescriptor => d !== null);
}

/** The registry id of an installed previewer. */
const VIEWER_ID_PREFIX = "component.previewer.";

export function previewerViewerId(name: string): string {
  return `${VIEWER_ID_PREFIX}${name}`;
}

/** Whether a registry viewer id is an installed previewer's (not a built-in). */
export function isPreviewerViewerId(id: string | undefined): boolean {
  return id !== undefined && id.startsWith(VIEWER_ID_PREFIX);
}

/** Whether a descriptor's browser frame ticket must be refreshed first. */
export function frameTicketStale(desc: Pick<PreviewerDescriptor, "frameExpiresAtMs">, nowMs: number): boolean {
  return desc.frameExpiresAtMs - FRAME_TICKET_SLACK_MS <= nowMs;
}

export type { ConvertFileSrc };

/** The iframe src of a previewer: the desktop scheme URL when the shell's
 *  convertFileSrc is present, else the gateway frame route (apiUrl-prefixed
 *  for a configured gateway base). Always carries `#host=<origin>`. */
export function previewerFrameUrl(
  desc: Pick<PreviewerDescriptor, "name" | "version" | "entry" | "frameUrl">,
  env: FrameEnv,
): string {
  return frameSrc({ schemePath: `${desc.name}/${desc.version}/${desc.entry}`, gatewayPath: desc.frameUrl + desc.entry }, env);
}

/** Where a frame host runs: the desktop shell (convertFileSrc), else a
 *  browser on the gateway (apiUrl, the document's base). */
export interface FrameEnv {
  readonly convertFileSrc?: ConvertFileSrc;
  readonly apiUrl: (path: string) => string;
  readonly baseURI: string;
  readonly hostOrigin: string;
}

/** The iframe src of a frame bundle's document (a previewer's, a settings
 *  page's): its path on the desktop's bnbloc-previewer scheme, or its
 *  ticketed gateway path in a browser; always with `#host=<origin>`. */
export function frameSrc(at: { readonly schemePath: string; readonly gatewayPath: string }, env: FrameEnv): string {
  const base = env.convertFileSrc
    ? schemeUrl(env.convertFileSrc, PREVIEWER_SCHEME, at.schemePath)
    : new URL(env.apiUrl(at.gatewayPath), env.baseURI).href;
  return `${base}#host=${encodeURIComponent(env.hostOrigin)}`;
}

/** Whether an inbound message event comes from a sandboxed frame's window:
 *  the opaque origin "null" (such a frame's only possible origin) and the
 *  frame's own window. Every frame host (a previewer, a settings page)
 *  takes messages only so. */
export function fromFrame(ev: { origin: string; source: unknown }, frameWindow: unknown): boolean {
  return ev.origin === "null" && !!frameWindow && ev.source === frameWindow;
}

/** Validates one inbound message event against the frame it must come from
 *  (fromFrame) and as a known, well-formed, in-limit message. */
export function readPreviewerMessage(
  ev: { origin: string; source: unknown; data: unknown },
  frameWindow: unknown,
): FrameMessage | null {
  return fromFrame(ev, frameWindow) ? readFrameMessage(ev.data) : null;
}

/** Whether the host takes a thumbnail the previewer produced: only when it
 *  declares the capability (where it goes, and whether the version still
 *  needs one, is the host's: ArtifactBody's onCover). */
export function wantsThumbnail(desc: Pick<PreviewerDescriptor, "capabilities">): boolean {
  return desc.capabilities.includes("thumbnail");
}

/** Why the host refuses a frame's save (null: it may publish). A previewer
 *  saves only with the "edit" capability it declared, and only a room's
 *  artifact: a chat attachment has no versions, a public share page no room. */
export function saveRefusal(
  desc: Pick<PreviewerDescriptor, "capabilities">,
  at: { readonly roomId: string; readonly artifactId: string },
): string | null {
  if (!desc.capabilities.includes("edit")) return "This previewer cannot save files.";
  if (!at.roomId || !at.artifactId) return "This file cannot be saved here.";
  return null;
}

/** What a failed save tells the frame. A 409 is the gateway's compare-and-swap
 *  refusal: the file moved on since the frame was handed it. Nothing is merged;
 *  the member reloads the file and saves again. */
export function saveFailure(err: unknown): string {
  const text =
    err instanceof ApiError && err.status === 409
      ? "changed meanwhile: a newer version exists; reload the file to take it."
      : errorText(err, "Could not save the file");
  // The frame reads a message of at most ERROR_LIMIT characters; a longer one
  // would be dropped unseen, so it is cut here.
  return [...text].slice(0, ERROR_LIMIT).join("");
}

/** What the host does with a frame's message when the frame is shown to
 *  someone who only looks (a shared link): null means handle it as usual; a
 *  refusal drops the message, and answers it when the frame waits for an
 *  answer (a save, a draft), so it does not sit on "Saving…". The host decides
 *  this itself, whatever the frame declared or its init said: a previewer that
 *  predates `viewOnly` must not be able to write through a share. */
export function viewOnlyRefusal(type: FrameMessage["type"]): { readonly reply: HostMessage | null } | null {
  const message = "This file is shown for viewing only.";
  switch (type) {
    case "preview.save":
      return { reply: { type: "preview.save.failed", message } };
    case "preview.draft":
      return { reply: { type: "preview.draft.failed", message } };
    case "preview.state.send":
    case "preview.selection":
      return { reply: null };
    default:
      return null;
  }
}

/** Whether a draft reply fits the frame's limit: the frame refuses a larger
 *  one, so the host says so itself rather than send a reply nobody reads. */
export function draftReplyFits(reply: Rec): boolean {
  return new TextEncoder().encode(JSON.stringify(reply)).length <= DRAFT_REPLY_LIMIT;
}

/** What a failed draft tells the frame, within the frame's message limit. */
export function draftFailure(err: unknown): string {
  return [...errorText(err, "Could not draft the content")].slice(0, ERROR_LIMIT).join("");
}

/** Why the host refuses a frame's draft request (null: it may run the task).
 *  A previewer drafts only with the "draft" capability and a task it declared. */
export function draftRefusal(desc: Pick<PreviewerDescriptor, "capabilities" | "draftTask">): string | null {
  if (!desc.capabilities.includes("draft") || !desc.draftTask) return "This previewer cannot draft content.";
  return null;
}

/** Theme from the app's background lightness ("hsl(h s% l%)" or "#rrggbb"). */
export function themeFromBackground(color: string): "light" | "dark" {
  const hsl = /hsla?\(\s*[\d.]+(?:deg)?[\s,]+[\d.]+%[\s,]+([\d.]+)%/.exec(color);
  if (hsl) return Number(hsl[1]) < 50 ? "dark" : "light";
  const hex = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})/i.exec(color.trim());
  if (hex) {
    const [r, g, b] = [hex[1], hex[2], hex[3]].map((h) => parseInt(h, 16));
    return 0.2126 * r + 0.7152 * g + 0.0722 * b < 128 ? "dark" : "light";
  }
  return "light";
}
