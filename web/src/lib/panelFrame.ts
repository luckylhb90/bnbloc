// Host side of a plugin's own panel (docs/architecture/plugin-panels.md):
// where the page loads from, which messages it may send, and what a call of
// its does. Pure (no React, no DOM globals) so it is unit-tested under node;
// PanelDialog wires it to the frame. The messages are the SDK's panel
// protocol, the one definition both sides import; the frame is the
// previewer's (the same scheme, the same sandbox, the same sender rule).
//
// A panel has no network and no credential. Each call it makes is a call to
// its own plugin's loopback API, made by the desktop with the bearer the
// desktop keeps: what a path may be, and which plugin it reaches, is the
// desktop's rule, so a call it refuses comes back here as an error and goes to
// the page as a refusal with its reason, never a different answer.

import {
  fitPanelReason,
  PANEL_PROTOCOL,
  readPanelMessage,
  type PanelHostMessage,
  type PanelMessage,
  type PanelMethod,
} from "../../previewer-sdk/src/panelProtocol.ts";
import type { Theme } from "../../previewer-sdk/src/protocol.ts";
import { errorReason } from "./errors.ts";
import { frameSrc, fromFrame, validBundlePath, type FrameEnv } from "./previewerFrame.ts";

/** Where a panel loads from: its entry and bundle pin (the desktop serves the
 *  bundle by its pin). */
export interface PanelSource {
  readonly entry: string;
  readonly bundle: { readonly sha256: string };
}

/** The iframe src of a panel, or null where it cannot load: a panel runs
 *  where its plugin does, on the desktop. */
export function panelSrc(page: PanelSource, env: FrameEnv): string | null {
  if (!validBundlePath(page.entry) || !env.convertFileSrc) return null;
  return frameSrc({ schemePath: `_page/${page.bundle.sha256}/${page.entry}`, gatewayPath: "" }, env);
}

/** A page message from the panel's own frame, validated; null otherwise. */
export function readPanelFrameMessage(ev: { origin: string; source: unknown; data: unknown }, frameWindow: unknown): PanelMessage | null {
  return fromFrame(ev, frameWindow) ? readPanelMessage(ev.data) : null;
}

/** The answer to the page's ready: the protocol and the theme. */
export function panelInit(theme: Theme): PanelHostMessage {
  return { type: "panel.init", protocol: PANEL_PROTOCOL, theme };
}

/** One call to the panel's plugin: its status and body whatever the status;
 *  a call that never reached it throws. */
export interface PanelCalls {
  readonly call: (method: PanelMethod, path: string, body?: unknown) => Promise<{ readonly status: number; readonly body: unknown }>;
}

/** The host of one panel frame: answers each call of the page (ready and
 *  close are the frame component's). */
export function panelHost(calls: PanelCalls): { answer(msg: PanelMessage): Promise<PanelHostMessage | null> } {
  return {
    async answer(msg) {
      if (msg.type !== "panel.call") return null;
      try {
        const { status, body } = await calls.call(msg.method, msg.path, msg.body);
        return { type: "panel.result", id: msg.id, status, body };
      } catch (e) {
        return { type: "panel.refused", id: msg.id, reason: fitPanelReason(errorReason(e) || "The call could not be made") };
      }
    },
  };
}
