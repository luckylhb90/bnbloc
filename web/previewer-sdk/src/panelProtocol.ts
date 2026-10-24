// The panel protocol (protocol 1): the ONE definition of every message
// between the host (the bnbloc web client) and a plugin's own panel, a bundle
// the platform runs in the previewer's sandboxed opaque-origin frame on the
// member's desktop (docs/architecture/plugin-panels.md). Both sides import it:
// the host (web/src/lib/panelFrame.ts) and the page SDK (./panel.ts), so the
// shapes cannot drift.
//
// Pure: no DOM globals, no dependencies, so it is unit-tested under node.
//
//   frame -> host : panel.ready                                   (once, handlers registered)
//   host  -> frame: panel.init    {protocol, theme}               (the answer to ready)
//   frame -> host : panel.call    {id, method, path, body?}       (a call to the plugin's own API)
//   host  -> frame: panel.result  {id, status, body}              (the plugin's answer, whatever its status)
//   host  -> frame: panel.refused {id, reason}                    (the call never reached the plugin)
//   frame -> host : panel.close
//
// A panel has no network and no credential: its calls are the plugin's
// loopback API under /v1/, made by the desktop with the bearer it keeps. What
// a path may be, and which plugin it reaches, is the desktop's rule (the one
// relay): this protocol only bounds a message, and a call the relay refuses
// comes back as panel.refused with its reason.

import type { Theme } from "./protocol.ts";

/** Protocol version carried in panel.init. */
export const PANEL_PROTOCOL = 1;

/** The methods a panel may call with. */
export const PANEL_METHODS = ["GET", "POST", "PUT", "DELETE"] as const;
export type PanelMethod = (typeof PANEL_METHODS)[number];

/** Message bounds: a path, and a request body as JSON text. */
export const PANEL_PATH_LIMIT = 2048;
export const PANEL_BODY_LIMIT = 1024 * 1024;
export const PANEL_REASON_LIMIT = 500;

export type PanelMessage =
  | { readonly type: "panel.ready" }
  | { readonly type: "panel.call"; readonly id: number; readonly method: PanelMethod; readonly path: string; readonly body?: unknown }
  | { readonly type: "panel.close" };

export type PanelHostMessage =
  | { readonly type: "panel.init"; readonly protocol: number; readonly theme: Theme }
  | { readonly type: "panel.result"; readonly id: number; readonly status: number; readonly body: unknown }
  | { readonly type: "panel.refused"; readonly id: number; readonly reason: string };

type Rec = Readonly<Record<string, unknown>>;

function isRecord(value: unknown): value is Rec {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requestId(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : null;
}

function isMethod(value: unknown): value is PanelMethod {
  return typeof value === "string" && (PANEL_METHODS as readonly string[]).includes(value);
}

/** The size of a body as JSON text; null when it is not JSON. */
export function bodyBytes(body: unknown): number | null {
  if (body === undefined) return 0;
  try {
    const text = JSON.stringify(body);
    return text === undefined ? null : new TextEncoder().encode(text).length;
  } catch {
    return null;
  }
}

/** Validates a frame -> host message (the host also checks the sender: see
 *  web/src/lib/panelFrame.ts). Null for anything unknown or malformed. */
export function readPanelMessage(data: unknown): PanelMessage | null {
  if (!isRecord(data)) return null;
  switch (data.type) {
    case "panel.ready":
    case "panel.close":
      return { type: data.type };
    case "panel.call": {
      const id = requestId(data.id);
      if (id === null || !isMethod(data.method) || typeof data.path !== "string" || data.path.length > PANEL_PATH_LIMIT) return null;
      const size = bodyBytes(data.body);
      if (size === null || size > PANEL_BODY_LIMIT) return null;
      return { type: "panel.call", id, method: data.method, path: data.path, ...(data.body !== undefined ? { body: data.body } : {}) };
    }
    default:
      return null;
  }
}

/** Validates a host -> frame message (the page SDK also checks the sender).
 *  Null for anything unknown or malformed. */
export function readPanelHostMessage(data: unknown): PanelHostMessage | null {
  if (!isRecord(data)) return null;
  switch (data.type) {
    case "panel.init":
      if (data.protocol !== PANEL_PROTOCOL || (data.theme !== "light" && data.theme !== "dark")) return null;
      return { type: "panel.init", protocol: PANEL_PROTOCOL, theme: data.theme };
    case "panel.result": {
      const id = requestId(data.id);
      return id === null || typeof data.status !== "number" || !Number.isInteger(data.status) ? null : { type: "panel.result", id, status: data.status, body: data.body };
    }
    case "panel.refused": {
      const id = requestId(data.id);
      if (id === null || typeof data.reason !== "string" || [...data.reason].length > PANEL_REASON_LIMIT) return null;
      return { type: "panel.refused", id, reason: data.reason };
    }
    default:
      return null;
  }
}

/** Cuts a refusal's reason to the protocol bound. */
export function fitPanelReason(reason: string): string {
  return [...reason].slice(0, PANEL_REASON_LIMIT).join("");
}
