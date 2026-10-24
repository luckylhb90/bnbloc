// @bnbloc/previewer-sdk/panel — the page side of the panel protocol
// (./panelProtocol.ts, docs/architecture/plugin-panels.md): a plugin's own
// panel, running in the previewer's sandboxed frame on the member's desktop,
// reaches its plugin ONLY through it:
//
//   import { onPanelInit, callPanel, PanelRefused } from "@bnbloc/previewer-sdk/panel";
//   onPanelInit(({ theme }) => applyTheme(theme));
//   const { status, body } = await callPanel("GET", "/v1/state");
//
// Intake is fail-closed as the previewer client's (./index.ts): only
// window.parent, from the origin the frame URL names (#host=), passing the
// protocol validator. Importing this module never starts a previewer client.

import type { PreviewerEnv, Unsubscribe } from "./index.ts";
import { hostOriginFromHash, type Theme } from "./protocol.ts";
import { bodyBytes, PANEL_BODY_LIMIT, PANEL_PATH_LIMIT, readPanelHostMessage, type PanelHostMessage, type PanelMethod } from "./panelProtocol.ts";

export * from "./panelProtocol.ts";

/** What panel.init carried: the theme. */
export interface PanelInit {
  readonly theme: Theme;
}

/** The plugin's answer to a call, whatever its status (a 409 is an answer). */
export interface PanelResult {
  readonly status: number;
  readonly body: unknown;
}

/** The call never reached the plugin: the desktop's reason (a path outside
 *  /v1/, a plugin that is not running). */
export class PanelRefused extends Error {
  constructor(reason: string) {
    super(reason);
    this.name = "PanelRefused";
  }
}

export interface PanelClient {
  /** False when not framed or no host origin: nothing is accepted or posted. */
  readonly active: boolean;
  /** Posts panel.ready (once). */
  announceReady(): void;
  onInit(cb: (init: PanelInit) => void): Unsubscribe;
  /** One call to the plugin's own API; rejects with PanelRefused when it never got there. */
  call(method: PanelMethod, path: string, body?: unknown): Promise<PanelResult>;
  /** Asks the host to close the panel. */
  close(): void;
}

type Answer = Exclude<PanelHostMessage, { type: "panel.init" }>;

/** Builds a client over an environment (a browser window, fakes in tests).
 *  An onInit registered after init still sees it. */
export function createPanelClient(env: PreviewerEnv): PanelClient {
  const active = env.parent !== null && env.hostOrigin !== "";
  const initHandlers: ((init: PanelInit) => void)[] = [];
  const pending = new Map<number, (answer: Answer) => void>();
  let init: PanelInit | null = null;
  let ready = false;
  let nextId = 1;

  // Outbound "*" as the previewer client's: the parent is pinned by the
  // document's frame-ancestors, and nothing a page posts is secret.
  const post = (message: unknown): void => {
    if (active && env.parent) env.parent.postMessage(message, "*");
  };

  if (active) {
    env.addMessageListener((ev) => {
      if (ev.source !== env.parent || ev.origin !== env.hostOrigin) return;
      const msg = readPanelHostMessage(ev.data);
      if (!msg) return;
      if (msg.type === "panel.init") {
        init = { theme: msg.theme };
        for (const cb of [...initHandlers]) cb(init);
        return;
      }
      const resolve = pending.get(msg.id);
      if (!resolve) return;
      pending.delete(msg.id);
      resolve(msg);
    });
  }

  return {
    active,
    announceReady() {
      if (ready || !active) return;
      ready = true;
      post({ type: "panel.ready" });
    },
    onInit(cb) {
      initHandlers.push(cb);
      if (init) cb(init);
      return () => {
        const i = initHandlers.indexOf(cb);
        if (i >= 0) initHandlers.splice(i, 1);
      };
    },
    async call(method, path, body) {
      if (!active) throw new Error("@bnbloc/previewer-sdk/panel: not in a panel frame");
      const size = bodyBytes(body);
      if (path.length > PANEL_PATH_LIMIT || size === null || size > PANEL_BODY_LIMIT) throw new Error("callPanel: the path or the body is outside the panel protocol's bounds");
      const id = nextId++;
      const answer = await new Promise<Answer>((resolve) => {
        pending.set(id, resolve);
        post({ type: "panel.call", id, method, path, ...(body !== undefined ? { body } : {}) });
      });
      if (answer.type === "panel.refused") throw new PanelRefused(answer.reason);
      return { status: answer.status, body: answer.body };
    },
    close: () => post({ type: "panel.close" }),
  };
}

function windowClient(): PanelClient {
  const framed = window.parent !== window;
  const client = createPanelClient({
    parent: framed ? window.parent : null,
    hostOrigin: hostOriginFromHash(window.location.hash),
    addMessageListener: (listener) => window.addEventListener("message", listener),
  });
  // Ready on window load: every page script has registered its handlers.
  if (document.readyState === "complete") setTimeout(() => client.announceReady(), 0);
  else window.addEventListener("load", () => client.announceReady(), { once: true });
  return client;
}

const defaultClient: PanelClient | null = typeof window !== "undefined" && typeof document !== "undefined" ? windowClient() : null;

function client(): PanelClient {
  if (!defaultClient) throw new Error("@bnbloc/previewer-sdk/panel: no browser window (use createPanelClient)");
  return defaultClient;
}

export const onPanelInit = (cb: (init: PanelInit) => void): Unsubscribe => client().onInit(cb);
export const callPanel = (method: PanelMethod, path: string, body?: unknown): Promise<PanelResult> => client().call(method, path, body);
export const closePanel = (): void => client().close();
