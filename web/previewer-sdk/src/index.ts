// @bnbloc/previewer-sdk — the frame side of the previewer frame protocol
// (bnbloc.previewer.contract/v1, see ./protocol.ts for every message shape).
//
// A previewer bundle runs in a sandbox="allow-scripts" iframe (opaque origin,
// CSP default-src 'none', no network). It talks to the bnbloc host ONLY
// through this SDK:
//
//   import { onFile, setToolbar, onToolbar, provideThumbnail } from "@bnbloc/previewer-sdk";
//   onFile((bytes, file) => render(bytes, file.mimeType));
//
// Intake is fail-closed: a message is accepted only when it comes from
// window.parent, from the host origin the frame URL names in its fragment
// (#host=<origin>), and passes the protocol validator. Without a host origin
// the client refuses everything and never posts.
//
// The core is createPreviewerClient(env), DOM-free so it is unit-tested under
// node; the module-level functions bind one client to `window` when imported
// in a browser.

import {
  ERROR_LIMIT,
  TITLE_LIMIT,
  fitSelection,
  hostOriginFromHash,
  readHostMessage,
  readToolbar,
  stateKey,
  validThumbnail,
  type HostMessage,
  type PreviewFile,
  type Theme,
  type ToolbarItem,
  type Viewport,
} from "./protocol.ts";

export * from "./protocol.ts";

/** What preview.init carried: the file's metadata, theme and viewport. */
export interface PreviewInit {
  readonly file: PreviewFile;
  readonly theme: Theme;
  readonly viewport: Viewport;
}

/** The minimal message event the client inspects. */
export interface InboundEvent {
  readonly origin: string;
  readonly source: unknown;
  readonly data: unknown;
}

export interface MessageTarget {
  postMessage(message: unknown, targetOrigin: string, transfer?: Transferable[]): void;
}

/** Everything the client needs from its environment (a browser window in
 *  production, fakes in tests). */
export interface PreviewerEnv {
  /** The host window (window.parent), or null when not framed. */
  readonly parent: MessageTarget | null;
  /** The origin named by the frame URL's #host= fragment ("" = none). */
  readonly hostOrigin: string;
  readonly addMessageListener: (listener: (ev: InboundEvent) => void) => void;
}

export type InitHandler = (init: PreviewInit) => void;
export type FileHandler = (bytes: ArrayBuffer, file: PreviewFile) => void;
export type ToolbarHandler = (id: string, value: boolean | number | null) => void;
export type StateHandler = (state: unknown, actor: string) => void;
export type ResizeHandler = (viewport: Viewport) => void;
export type Unsubscribe = () => void;

export interface PreviewerClient {
  /** False when the frame is not framed or names no host origin: nothing is
   *  accepted and nothing is posted. */
  readonly active: boolean;
  /** Posts preview.ready (once; later calls are no-ops). */
  announceReady(): void;
  onInit(cb: InitHandler): Unsubscribe;
  onFile(cb: FileHandler): Unsubscribe;
  onToolbar(cb: ToolbarHandler): Unsubscribe;
  onState(cb: StateHandler): Unsubscribe;
  onResize(cb: ResizeHandler): Unsubscribe;
  setToolbar(items: readonly ToolbarItem[]): void;
  sendState(state: unknown): void;
  provideThumbnail(png: Blob | ArrayBuffer): Promise<void>;
  setTitle(text: string): void;
  reportError(message: string): void;
  /** Points the host at a part of the file (capability "selection"): a
   *  chip it shows beside its Ask AI composer, whose detail travels to the
   *  agent with the request; null clears it. Over-long text is cut. */
  sendSelection(selection: { label: string; detail?: string } | null): void;
}

function subscribe<T>(list: T[], cb: T): Unsubscribe {
  list.push(cb);
  return () => {
    const i = list.indexOf(cb);
    if (i >= 0) list.splice(i, 1);
  };
}

/** Builds a client over an environment. Handlers registered after an event
 *  still see it: the last init is replayed to late onInit/onFile handlers and
 *  a file that arrives before any onFile handler is buffered. */
export function createPreviewerClient(env: PreviewerEnv): PreviewerClient {
  const active = env.parent !== null && env.hostOrigin !== "";
  const initHandlers: InitHandler[] = [];
  const fileHandlers: FileHandler[] = [];
  const toolbarHandlers: ToolbarHandler[] = [];
  const stateHandlers: StateHandler[] = [];
  const resizeHandlers: ResizeHandler[] = [];
  let init: PreviewInit | null = null;
  let file: ArrayBuffer | null = null;
  let ready = false;

  // Outbound messages use targetOrigin "*", like the artifact runner: a
  // custom-scheme app origin (the desktop shell's tauri://localhost) is not
  // reliably matchable as a targetOrigin across engines, and the parent is
  // already pinned by the document's CSP frame-ancestors. Nothing posted is
  // secret (it is the previewer's own output); inbound messages stay pinned
  // to the host origin below.
  const post = (message: unknown, transfer?: Transferable[]): void => {
    if (!active || !env.parent) return;
    env.parent.postMessage(message, "*", transfer);
  };

  const deliverFile = (): void => {
    if (!init || !file || fileHandlers.length === 0) return;
    const bytes = file;
    const meta = init.file;
    file = null;
    for (const cb of [...fileHandlers]) cb(bytes, meta);
  };

  const handle = (msg: HostMessage): void => {
    switch (msg.type) {
      case "preview.init":
        init = { file: msg.file, theme: msg.theme, viewport: msg.viewport };
        for (const cb of [...initHandlers]) cb(init);
        deliverFile();
        return;
      case "preview.file":
        file = msg.bytes;
        deliverFile();
        return;
      case "preview.toolbar":
        for (const cb of [...toolbarHandlers]) cb(msg.id, msg.value);
        return;
      case "preview.state":
        for (const cb of [...stateHandlers]) cb(msg.state, msg.actor);
        return;
      case "preview.resize":
        if (init) init = { ...init, viewport: { w: msg.w, h: msg.h } };
        for (const cb of [...resizeHandlers]) cb({ w: msg.w, h: msg.h });
        return;
    }
  };

  if (active) {
    env.addMessageListener((ev) => {
      if (ev.source !== env.parent || ev.origin !== env.hostOrigin) return;
      const msg = readHostMessage(ev.data);
      if (msg) handle(msg);
    });
  }

  return {
    active,
    announceReady() {
      if (ready || !active) return;
      ready = true;
      post({ type: "preview.ready" });
    },
    onInit(cb) {
      const off = subscribe(initHandlers, cb);
      if (init) cb(init);
      return off;
    },
    onFile(cb) {
      const off = subscribe(fileHandlers, cb);
      deliverFile();
      return off;
    },
    onToolbar: (cb) => subscribe(toolbarHandlers, cb),
    onState: (cb) => subscribe(stateHandlers, cb),
    onResize: (cb) => subscribe(resizeHandlers, cb),
    setToolbar(items) {
      const valid = readToolbar(items);
      if (!valid) throw new Error("setToolbar: invalid toolbar items (see the previewer contract)");
      post({ type: "preview.toolbar.set", items: valid });
    },
    sendState(state) {
      if (stateKey(state) === null) throw new Error("sendState: state must be JSON and at most 16 KiB");
      post({ type: "preview.state.send", state });
    },
    async provideThumbnail(png) {
      const bytes = png instanceof ArrayBuffer ? png : await png.arrayBuffer();
      if (!validThumbnail(bytes)) throw new Error("provideThumbnail: thumbnail must be a PNG of at most 512 KiB");
      post({ type: "preview.thumbnail", png: bytes }, [bytes]);
    },
    setTitle(text) {
      if (typeof text !== "string" || [...text].length > TITLE_LIMIT) {
        throw new Error(`setTitle: title must be a string of at most ${TITLE_LIMIT} characters`);
      }
      post({ type: "preview.title", text });
    },
    reportError(message) {
      // Reporting must never throw: clamp instead of refusing.
      const text = String(message ?? "").trim() || "previewer error";
      post({ type: "preview.error", message: [...text].slice(0, ERROR_LIMIT).join("") });
    },
    sendSelection(selection) {
      post({ type: "preview.selection", selection: selection ? fitSelection(String(selection.label ?? ""), String(selection.detail ?? "")) : null });
    },
  };
}

function windowClient(): PreviewerClient {
  const framed = window.parent !== window;
  const client = createPreviewerClient({
    parent: framed ? window.parent : null,
    hostOrigin: hostOriginFromHash(window.location.hash),
    addMessageListener: (listener) => window.addEventListener("message", listener),
  });
  // Ready on window load: every bundle script has run and registered its
  // handlers by then, so the host's init/file cannot outrun them.
  if (document.readyState === "complete") setTimeout(() => client.announceReady(), 0);
  else window.addEventListener("load", () => client.announceReady(), { once: true });
  return client;
}

const defaultClient: PreviewerClient | null =
  typeof window !== "undefined" && typeof document !== "undefined" ? windowClient() : null;

function client(): PreviewerClient {
  if (!defaultClient) throw new Error("@bnbloc/previewer-sdk: no browser window (use createPreviewerClient)");
  return defaultClient;
}

export const onInit = (cb: InitHandler): Unsubscribe => client().onInit(cb);
export const onFile = (cb: FileHandler): Unsubscribe => client().onFile(cb);
export const onToolbar = (cb: ToolbarHandler): Unsubscribe => client().onToolbar(cb);
export const onState = (cb: StateHandler): Unsubscribe => client().onState(cb);
export const onResize = (cb: ResizeHandler): Unsubscribe => client().onResize(cb);
export const setToolbar = (items: readonly ToolbarItem[]): void => client().setToolbar(items);
export const sendState = (state: unknown): void => client().sendState(state);
export const provideThumbnail = (png: Blob | ArrayBuffer): Promise<void> => client().provideThumbnail(png);
export const setTitle = (text: string): void => client().setTitle(text);
export const reportError = (message: string): void => client().reportError(message);
export const sendSelection = (selection: { label: string; detail?: string } | null): void => client().sendSelection(selection);
