// @bnbloc/previewer-sdk/settings — the page side of the settings page
// protocol (./settingsProtocol.ts, docs/architecture/settings-pages.md): a
// component's own settings page, running in the previewer's sandboxed frame,
// reads and saves its settings ONLY through it:
//
//   import { onSettingsInit, getSettings, setSettings, SettingsRefused } from "@bnbloc/previewer-sdk/settings";
//   onSettingsInit(({ theme }) => applyTheme(theme));
//   render(await getSettings());
//   try { render(await setSettings({ apiBase: url })); } catch (e) { if (e instanceof SettingsRefused) showAt(e.key, e.message); }
//
// Intake is fail-closed as the previewer client's (./index.ts): only
// window.parent, from the origin the frame URL names (#host=), passing the
// protocol validator. Importing this module never starts a previewer client.

import type { PreviewerEnv, Unsubscribe } from "./index.ts";
import { hostOriginFromHash, type Theme } from "./protocol.ts";
import { readHostAnswer, readUpdate, type HostAnswer, type SettingsPageUpdate, type SettingsPageView } from "./settingsProtocol.ts";

export * from "./settingsProtocol.ts";

/** What settings.init carried: the theme, and the scope the page edits. */
export interface SettingsPageInit {
  readonly theme: Theme;
  readonly scope: string;
}

/** The platform refused a save (or a read): its reason, and the setting it
 *  is about when it names one. */
export class SettingsRefused extends Error {
  readonly key?: string;
  constructor(reason: string, key?: string) {
    super(reason);
    this.name = "SettingsRefused";
    if (key !== undefined) this.key = key;
  }
}

/** Whether the component's own check ran (§2.5 of the PRD): a host that
 *  cannot run one answers supported false. */
export interface SettingsCheck {
  readonly supported: boolean;
}

export interface SettingsPageClient {
  /** False when not framed or no host origin: nothing is accepted or posted. */
  readonly active: boolean;
  /** Posts settings.ready (once). */
  announceReady(): void;
  onInit(cb: (init: SettingsPageInit) => void): Unsubscribe;
  /** The scope's settings as they are stored (a secret only as set). */
  get(): Promise<SettingsPageView>;
  /** Saves values (and clears keys); answers the view as stored, or rejects
   *  with SettingsRefused. */
  set(values: Readonly<Record<string, string>>, clear?: readonly string[]): Promise<SettingsPageView>;
  check(): Promise<SettingsCheck>;
  /** Asks the host to close the page. */
  close(): void;
}

type Answer = Exclude<HostAnswer, { type: "settings.init" }>;

/** Builds a client over an environment (a browser window, fakes in tests).
 *  An onInit registered after init still sees it. */
export function createSettingsPageClient(env: PreviewerEnv): SettingsPageClient {
  const active = env.parent !== null && env.hostOrigin !== "";
  const initHandlers: ((init: SettingsPageInit) => void)[] = [];
  const pending = new Map<number, (answer: Answer) => void>();
  let init: SettingsPageInit | null = null;
  let ready = false;
  let nextId = 1;

  // Outbound "*" as the previewer client's: the parent is pinned by the
  // document's frame-ancestors, and nothing a page posts is secret.
  const post = (message: unknown): void => {
    if (active && env.parent) env.parent.postMessage(message, "*");
  };
  const request = (message: Record<string, unknown>): Promise<Answer> => {
    if (!active) return Promise.reject(new Error("@bnbloc/previewer-sdk/settings: not in a settings page frame"));
    const id = nextId++;
    return new Promise((resolve) => {
      pending.set(id, resolve);
      post({ ...message, id });
    });
  };
  // The answer of the type asked for; a refusal throws SettingsRefused.
  const expect = <T extends Answer["type"]>(answer: Answer, type: T): Extract<Answer, { type: T }> => {
    if (answer.type === "settings.refused") throw new SettingsRefused(answer.reason, answer.key);
    if (answer.type !== type) throw new Error(`unexpected answer ${answer.type}`);
    return answer as Extract<Answer, { type: T }>;
  };

  if (active) {
    env.addMessageListener((ev) => {
      if (ev.source !== env.parent || ev.origin !== env.hostOrigin) return;
      const msg = readHostAnswer(ev.data);
      if (!msg) return;
      if (msg.type === "settings.init") {
        init = { theme: msg.theme, scope: msg.scope };
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
      post({ type: "settings.ready" });
    },
    onInit(cb) {
      initHandlers.push(cb);
      if (init) cb(init);
      return () => {
        const i = initHandlers.indexOf(cb);
        if (i >= 0) initHandlers.splice(i, 1);
      };
    },
    get: async () => expect(await request({ type: "settings.get" }), "settings.view").view,
    async set(values, clear = []) {
      const update: SettingsPageUpdate | null = readUpdate(values, clear);
      if (!update) throw new Error("setSettings: values must be strings by key (see the settings page protocol)");
      return expect(await request({ type: "settings.set", ...update }), "settings.view").view;
    },
    async check() {
      return { supported: expect(await request({ type: "settings.check" }), "settings.checked").supported };
    },
    close: () => post({ type: "settings.close" }),
  };
}

function windowClient(): SettingsPageClient {
  const framed = window.parent !== window;
  const client = createSettingsPageClient({
    parent: framed ? window.parent : null,
    hostOrigin: hostOriginFromHash(window.location.hash),
    addMessageListener: (listener) => window.addEventListener("message", listener),
  });
  // Ready on window load: every page script has registered its handlers.
  if (document.readyState === "complete") setTimeout(() => client.announceReady(), 0);
  else window.addEventListener("load", () => client.announceReady(), { once: true });
  return client;
}

const defaultClient: SettingsPageClient | null =
  typeof window !== "undefined" && typeof document !== "undefined" ? windowClient() : null;

function client(): SettingsPageClient {
  if (!defaultClient) throw new Error("@bnbloc/previewer-sdk/settings: no browser window (use createSettingsPageClient)");
  return defaultClient;
}

export const onSettingsInit = (cb: (init: SettingsPageInit) => void): Unsubscribe => client().onInit(cb);
export const getSettings = (): Promise<SettingsPageView> => client().get();
export const setSettings = (values: Readonly<Record<string, string>>, clear?: readonly string[]): Promise<SettingsPageView> =>
  client().set(values, clear);
export const checkSettings = (): Promise<SettingsCheck> => client().check();
export const closeSettings = (): void => client().close();
