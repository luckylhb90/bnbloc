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
  DRAFT_PROMPT_LIMIT,
  ERROR_LIMIT,
  SAVE_LIMIT,
  SAVE_SUMMARY_LIMIT,
  TITLE_LIMIT,
  fitSelection,
  fitUnits,
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
/** Shows a unit (its `at`): the host's "Show" on a change. */
export type RevealHandler = (at: string) => void;
export type SavedHandler = (version: number) => void;
export type SaveFailedHandler = (message: string) => void;
/** The assist task's JSON fields for a draft (its shape is the package's). */
export type DraftedHandler = (reply: Readonly<Record<string, unknown>>) => void;
export type DraftFailedHandler = (message: string) => void;
/** A selection as a previewer reports it (fitSelection bounds it). */
export type SelectionDraft = Parameters<typeof fitSelection>[0];
/** Reads a file into its units for Compare (capability "units"). */
export type UnitsHandler = (
  bytes: ArrayBuffer,
  file: PreviewFile,
) => readonly UnitDraft[] | Promise<readonly UnitDraft[]>;
/** A unit as a previewer makes it: fitUnits cuts it to the limits. */
export interface UnitDraft {
  readonly key?: string;
  readonly label: string;
  readonly text: string;
}
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
  sendSelection(selection: SelectionDraft | null): void;
  /** Answers the host's requests to read a version into units (capability
   *  "units", Compare): the last handler registered answers; a request that
   *  arrives first waits for it. A throw is reported to the host. */
  onUnits(cb: UnitsHandler): Unsubscribe;
  /** Scrolls to a unit the host asks to show (the `at` its units carried). */
  onReveal(cb: RevealHandler): Unsubscribe;
  /** Saves the file as its next version (capability "edit"): the bytes are
   *  transferred, so the caller must not use the buffer afterwards. Throws
   *  for empty or over-SAVE_LIMIT bytes; the summary is cut, not refused. */
  savePreview(bytes: ArrayBuffer, summary?: string): void;
  /** The host saved the bytes as this version. */
  onSaved(cb: SavedHandler): Unsubscribe;
  /** The host did not save the bytes (a conflict, a refusal, a limit). */
  onSaveFailed(cb: SaveFailedHandler): Unsubscribe;
  /** Asks the host to draft content from a sentence (capability "draft"); the
   *  prompt is trimmed. Throws for an empty or over-DRAFT_PROMPT_LIMIT prompt. */
  requestDraft(prompt: string): void;
  /** The host's draft: the assist task's fields, as the package declares them. */
  onDrafted(cb: DraftedHandler): Unsubscribe;
  /** The host did not draft (no model, a refusal, a reply too large). */
  onDraftFailed(cb: DraftFailedHandler): Unsubscribe;
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
  const revealHandlers: RevealHandler[] = [];
  const savedHandlers: SavedHandler[] = [];
  const saveFailedHandlers: SaveFailedHandler[] = [];
  const draftedHandlers: DraftedHandler[] = [];
  const draftFailedHandlers: DraftFailedHandler[] = [];
  // A reveal that arrives before the file is shown waits for it.
  let pendingReveal: string | null = null;
  let init: PreviewInit | null = null;
  let file: ArrayBuffer | null = null;
  let ready = false;
  let unitsHandler: UnitsHandler | null = null;
  const pendingUnits: Extract<HostMessage, { type: "preview.units.request" }>[] = [];

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

  const answerUnits = (req: Extract<HostMessage, { type: "preview.units.request" }>): void => {
    const handler = unitsHandler;
    if (!handler) {
      pendingUnits.push(req);
      return;
    }
    void Promise.resolve()
      .then(() => handler(req.bytes, req.file))
      .then(
        (list) => post({ type: "preview.units", id: req.id, ...fitUnits(list) }),
        (e: unknown) => {
          const text = String(e instanceof Error ? e.message : e ?? "").trim() || "could not read the file";
          post({ type: "preview.units", id: req.id, error: [...text].slice(0, ERROR_LIMIT).join("") });
        },
      );
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
      case "preview.units.request":
        answerUnits(msg);
        return;
      case "preview.reveal":
        if (revealHandlers.length === 0) pendingReveal = msg.at;
        for (const cb of [...revealHandlers]) cb(msg.at);
        return;
      case "preview.saved":
        for (const cb of [...savedHandlers]) cb(msg.version);
        return;
      case "preview.save.failed":
        for (const cb of [...saveFailedHandlers]) cb(msg.message);
        return;
      case "preview.drafted":
        for (const cb of [...draftedHandlers]) cb(msg.reply);
        return;
      case "preview.draft.failed":
        for (const cb of [...draftFailedHandlers]) cb(msg.message);
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
      post({ type: "preview.selection", selection: selection ? fitSelection(selection) : null });
    },
    onReveal(cb) {
      const off = subscribe(revealHandlers, cb);
      if (pendingReveal !== null) {
        const at = pendingReveal;
        pendingReveal = null;
        cb(at);
      }
      return off;
    },
    onUnits(cb) {
      unitsHandler = cb;
      for (const req of pendingUnits.splice(0)) answerUnits(req);
      return () => {
        if (unitsHandler === cb) unitsHandler = null;
      };
    },
    savePreview(bytes, summary) {
      if (!(bytes instanceof ArrayBuffer) || bytes.byteLength === 0 || bytes.byteLength > SAVE_LIMIT) {
        throw new Error(`savePreview: bytes must be a non-empty ArrayBuffer of at most ${SAVE_LIMIT} bytes`);
      }
      const text = summary === undefined ? undefined : [...String(summary)].slice(0, SAVE_SUMMARY_LIMIT).join("");
      post({ type: "preview.save", bytes, ...(text === undefined ? {} : { summary: text }) }, [bytes]);
    },
    onSaved: (cb) => subscribe(savedHandlers, cb),
    onSaveFailed: (cb) => subscribe(saveFailedHandlers, cb),
    requestDraft(prompt) {
      const text = typeof prompt === "string" ? prompt.trim() : "";
      if (text === "" || [...text].length > DRAFT_PROMPT_LIMIT) {
        throw new Error(`requestDraft: prompt must be a non-empty string of at most ${DRAFT_PROMPT_LIMIT} characters`);
      }
      post({ type: "preview.draft", prompt: text });
    },
    onDrafted: (cb) => subscribe(draftedHandlers, cb),
    onDraftFailed: (cb) => subscribe(draftFailedHandlers, cb),
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
export const sendSelection = (selection: SelectionDraft | null): void => client().sendSelection(selection);
export const onUnits = (cb: UnitsHandler): Unsubscribe => client().onUnits(cb);
export const onReveal = (cb: RevealHandler): Unsubscribe => client().onReveal(cb);
export const savePreview = (bytes: ArrayBuffer, summary?: string): void => client().savePreview(bytes, summary);
export const onSaved = (cb: SavedHandler): Unsubscribe => client().onSaved(cb);
export const onSaveFailed = (cb: SaveFailedHandler): Unsubscribe => client().onSaveFailed(cb);
export const requestDraft = (prompt: string): void => client().requestDraft(prompt);
export const onDrafted = (cb: DraftedHandler): Unsubscribe => client().onDrafted(cb);
export const onDraftFailed = (cb: DraftFailedHandler): Unsubscribe => client().onDraftFailed(cb);
