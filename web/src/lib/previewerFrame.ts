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
  PREVIEWER_CAPABILITIES,
  readFrameMessage,
  readToolbar,
  type FrameMessage,
  type PreviewerCapability,
  type ToolbarItem,
} from "../../previewer-sdk/src/protocol.ts";
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
  if (!caps.every((c) => (PREVIEWER_CAPABILITIES as readonly string[]).includes(c))) return null;
  const { entry: entryPath, rank, frameUrl, frameExpiresAtMs, bundleSha256 } = block;
  if (typeof entryPath !== "string" || !validBundlePath(entryPath)) return null;
  if (typeof rank !== "number" || !Number.isInteger(rank) || rank < 1 || rank > 1000) return null;
  if (typeof frameUrl !== "string" || !FRAME_URL_RE.test(frameUrl)) return null;
  if (typeof frameExpiresAtMs !== "number" || typeof bundleSha256 !== "string") return null;
  return {
    name,
    version,
    description: typeof entry.description === "string" ? entry.description : "",
    entry: entryPath,
    mimeTypes,
    extensions,
    rank,
    capabilities: caps as readonly PreviewerCapability[],
    toolbar,
    bundleSha256,
    frameUrl,
    frameExpiresAtMs,
  };
}

/** Parses the member catalog response (GET /v1/components?kind=previewer). */
export function parsePreviewerCatalog(body: unknown): PreviewerDescriptor[] {
  if (!isRecord(body) || !Array.isArray(body.components)) return [];
  return body.components.map(parsePreviewerEntry).filter((d): d is PreviewerDescriptor => d !== null);
}

/** The registry id of an installed previewer. */
export function previewerViewerId(name: string): string {
  return `component.previewer.${name}`;
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
  env: { convertFileSrc?: ConvertFileSrc; apiUrl: (path: string) => string; baseURI: string; hostOrigin: string },
): string {
  const base = env.convertFileSrc
    ? schemeUrl(env.convertFileSrc, PREVIEWER_SCHEME, `${desc.name}/${desc.version}/${desc.entry}`)
    : new URL(env.apiUrl(desc.frameUrl + desc.entry), env.baseURI).href;
  return `${base}#host=${encodeURIComponent(env.hostOrigin)}`;
}

/** Validates one inbound message event against the frame it must come from:
 *  the opaque origin "null" (a sandboxed frame's only possible origin), the
 *  frame's own window, and a known, well-formed, in-limit message. */
export function readPreviewerMessage(
  ev: { origin: string; source: unknown; data: unknown },
  frameWindow: unknown,
): FrameMessage | null {
  if (ev.origin !== "null") return null;
  if (!frameWindow || ev.source !== frameWindow) return null;
  return readFrameMessage(ev.data);
}

/** Whether the host should store a thumbnail the previewer produced: only
 *  when it declares the capability and the version has none yet. */
export function wantsThumbnail(
  desc: Pick<PreviewerDescriptor, "capabilities">,
  version: { readonly thumbnail?: unknown } | undefined,
): boolean {
  return desc.capabilities.includes("thumbnail") && version !== undefined && !version.thumbnail;
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
