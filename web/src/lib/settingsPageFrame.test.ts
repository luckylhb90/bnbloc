import { memberPageView, readSettingsPageMessage, scopePageView, settingsPageHost, settingsPageInit, settingsPageSrc, storedValues, type SettingsPageScope } from "./settingsPageFrame.ts";
import { ApiError } from "./errors.ts";
import type { ComponentSettingsUpdate, ComponentSettingsView, DesktopComponentSettings } from "./componentSettings.ts";
import type { HostAnswer, PageMessage } from "../../previewer-sdk/src/settingsProtocol.ts";

// Local runner (no node types in the web tsconfig), as the other tests.
async function test(name: string, fn: () => void | Promise<void>): Promise<void> {
  try {
    await fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

const SHA = "c".repeat(64);
const HOST = "https://app.example";

// The org view of a tool with a platform URL, an org key and page size, and
// a member folder; the secret's value is never in a view, but a page must not
// get one even if it were.
const orgView: ComponentSettingsView = {
  kind: "mcp",
  name: "notes",
  version: "1.0.0",
  scope: "org",
  available: true,
  reachesMembers: true,
  asks: false,
  fields: [
    { key: "apiKey", label: "API key", type: "secret", scope: "org", required: true, set: true, value: "s3cr3t" },
    { key: "pageSize", label: "Page size", type: "number", scope: "org", set: true, value: "50" },
  ],
  missing: [{ key: "apiBase", label: "API base URL", type: "url", scope: "platform", required: true }],
  page: { entry: "index.html", bundleSha256: SHA, frameUrl: "/v1/settings-pages/mcp/notes/1.0.0/T/", frameExpiresAtMs: 1 },
};

await test("the page loads from the desktop scheme by its pin, or the gateway's ticketed frame", () => {
  const convertFileSrc = (_: string, scheme: string) => `${scheme}://localhost/`;
  const env = { apiUrl: (p: string) => `https://gw.example${p}`, baseURI: "https://app.example/", hostOrigin: HOST };
  eq(
    settingsPageSrc({ entry: "index.html", bundleSha256: SHA }, { ...env, convertFileSrc }),
    `bnbloc-previewer://localhost/_page/${SHA}/index.html#host=${encodeURIComponent(HOST)}`,
    "desktop",
  );
  eq(settingsPageSrc(orgView.page!, env), `https://gw.example/v1/settings-pages/mcp/notes/1.0.0/T/index.html#host=${encodeURIComponent(HOST)}`, "browser");
  eq(settingsPageSrc({ entry: "index.html", bundleSha256: SHA }, env), null, "a browser needs a ticket");
  eq(settingsPageSrc({ entry: "../index.html", bundleSha256: SHA }, { ...env, convertFileSrc }), null, "a bad entry");
});

await test("only the page's own frame is heard", () => {
  const frame = {};
  const data = { type: "settings.get", id: 1 };
  eq(readSettingsPageMessage({ origin: "null", source: frame, data }, frame), data, "the frame");
  eq(readSettingsPageMessage({ origin: HOST, source: frame, data }, frame), null, "another origin");
  eq(readSettingsPageMessage({ origin: "null", source: {}, data }, frame), null, "another window");
  eq(readSettingsPageMessage({ origin: "null", source: frame, data: { type: "settings.set", id: 1, values: { a: 1 } } }, frame), null, "malformed");
});

await test("a page never reads a secret's value, nor another scope's", () => {
  const view = scopePageView(orgView);
  eq(view.fields[0], { key: "apiKey", label: "API key", type: "secret", required: true, set: true }, "secret only as set");
  eq(view.fields[1].value, "50", "a plain value");
  eq(view.readOnly, [{ key: "apiBase", label: "API base URL", type: "url", scope: "platform", set: false }], "the platform's, missing");
  eq(view.missing, ["apiBase"], "missing");
  const item: DesktopComponentSettings = {
    kind: "mcp",
    name: "notes",
    version: "1.0.0",
    state: "ready",
    message: "",
    auth: "",
    fields: [
      { key: "apiKey", label: "API key", type: "secret", scope: "org", required: true, set: true },
      { key: "folder", label: "Folder", type: "path", scope: "member", set: true, value: "/notes" },
      { key: "token", label: "Token", type: "secret", scope: "member", set: true, value: "t0k3n" },
    ],
    missing: [],
  };
  const mine = memberPageView(item);
  eq(mine.fields.map((f) => [f.key, f.value]), [["folder", "/notes"], ["token", undefined]], "member fields");
  eq(mine.readOnly, [{ key: "apiKey", label: "API key", type: "secret", scope: "org", set: true }], "the org's as set");
  if (JSON.stringify([view, mine]).includes("s3cr3t") || JSON.stringify(mine).includes("t0k3n")) throw new Error("a secret leaked");
});

function fakeScope(save: (u: ComponentSettingsUpdate) => ComponentSettingsView) {
  const saves: ComponentSettingsUpdate[] = [];
  const calls: SettingsPageScope = {
    load: async () => scopePageView(orgView),
    save: async (u) => {
      saves.push(u);
      return scopePageView(save(u));
    },
  };
  return { calls, saves };
}

await test("a save names only keys of the scope, and a refusal comes back at its field", async () => {
  const { calls, saves } = fakeScope((u) => {
    if (u.values.pageSize === "many") throw new ApiError(400, "Page size must be a number, such as 42 or 0.5", "pageSize");
    if (u.values.pageSize === "boom") throw new ApiError(400, "nope", "apiBase");
    return { ...orgView, fields: [orgView.fields[0], { ...orgView.fields[1], value: u.values.pageSize }] };
  });
  const host = settingsPageHost(calls);
  const set = (values: Record<string, string>, clear?: string[]): PageMessage => ({ type: "settings.set", id: 7, values, ...(clear ? { clear } : {}) });
  eq(await host.answer(set({ apiBase: "https://x.example.com" })), { type: "settings.refused", id: 7, reason: '"apiBase" is not a setting this page edits' }, "another scope");
  eq(await host.answer(set({ other: "x" })), { type: "settings.refused", id: 7, reason: '"other" is not a setting this page edits' }, "undeclared");
  eq(await host.answer(set({}, ["folder"])), { type: "settings.refused", id: 7, reason: '"folder" is not a setting this page edits' }, "clear another's");
  eq(saves.length, 0, "nothing sent");
  eq(await host.answer(set({ pageSize: "many" })), { type: "settings.refused", id: 7, reason: "Page size must be a number, such as 42 or 0.5", key: "pageSize" }, "the platform's reason");
  eq(await host.answer(set({ pageSize: "boom" })), { type: "settings.refused", id: 7, reason: "nope" }, "a key outside the page's fields is not echoed");
  const saved = (await host.answer(set({ pageSize: "10", apiKey: "" }, ["apiKey"]))) as Extract<HostAnswer, { type: "settings.view" }>;
  eq([saved.type, saved.view.fields[1].value], ["settings.view", "10"], "saved");
  eq(saves.at(-1), { values: { pageSize: "10", apiKey: "" }, clear: ["apiKey"] }, "the update as sent");
  eq(await host.answer({ type: "settings.check", id: 8 }), { type: "settings.checked", id: 8, supported: false }, "no check yet");
  eq(await host.answer({ type: "settings.close" }), null, "close is the frame's");
});

// A lines setting is a list on the page and its lines joined by "\n" in
// the store: the form's rule (trimmed, none empty) either way.
await test("a list goes to the page as its lines and back as the stored value", async () => {
  const item: DesktopComponentSettings = {
    kind: "plugin", name: "skills", version: "1.0.0", state: "running", message: "", auth: "", missing: [],
    fields: [{ key: "dirs", label: "Folders", type: "lines", scope: "member", set: true, value: "~/a\n~/b", default: "~/c" }],
  };
  eq(memberPageView(item).fields[0], { key: "dirs", label: "Folders", type: "lines", required: false, set: true, default: ["~/c"], value: ["~/a", "~/b"] }, "a list read");
  const fields = memberPageView(item).fields;
  eq(storedValues(fields, { dirs: [" ~/a ", "", "~/d"] }), { values: { dirs: "~/a\n~/d" } }, "a list stored");
  eq(storedValues(fields, { dirs: "~/a\n\n~/e" }), { values: { dirs: "~/a\n~/e" } }, "a string taken as lines");
  eq(storedValues([{ key: "note", type: "text" }], { note: ["x"] }), { notList: "note" }, "a list for a string");
  const saves: ComponentSettingsUpdate[] = [];
  const host = settingsPageHost({ load: async () => memberPageView(item), save: async (u) => (saves.push(u), memberPageView(item)) });
  await host.answer({ type: "settings.set", id: 1, values: { dirs: ["~/x", "~/y"] } });
  eq(saves, [{ values: { dirs: "~/x\n~/y" } }], "the update as sent");
});

// The example page (core/pkgmeta/testdata/settings-page/page.js), run in a
// fake frame against the host: it reads its scope, shows a secret only as
// set, shows a refusal at its field, and saves.
await test("the example page reads and saves through the host", async () => {
  const elements = new Map<string, FakeElement>();
  class FakeElement {
    children: FakeElement[] = [];
    attrs: Record<string, string> = {};
    textContent = "";
    name = "";
    type = "";
    value = "";
    placeholder = "";
    listeners: (() => void)[] = [];
    replaceChildren() {
      this.children = [];
    }
    appendChild(c: FakeElement) {
      this.children.push(c);
    }
    setAttribute(k: string, v: string) {
      this.attrs[k] = v;
    }
    addEventListener(_: string, cb: () => void) {
      this.listeners.push(cb);
    }
  }
  for (const id of ["fields", "status", "save", "close"]) elements.set(id, new FakeElement());
  const root = new FakeElement();
  const pageListeners: ((ev: { origin: string; source: unknown; data: unknown }) => void)[] = [];
  const received: unknown[] = [];
  const toPage = (data: unknown) => {
    received.push(data);
    pageListeners.forEach((l) => l({ origin: HOST, source: parent, data: structuredClone(data) }));
  };
  const { calls, saves } = fakeScope((u) => {
    if (u.values.pageSize !== "25") throw new ApiError(400, "Page size must be a number, such as 42 or 0.5", "pageSize");
    return { ...orgView, fields: [orgView.fields[0], { ...orgView.fields[1], value: "25" }] };
  });
  const host = settingsPageHost(calls);
  const posted: unknown[] = [];
  const pageWindow = {
    location: { hash: `#host=${encodeURIComponent(HOST)}` },
    addEventListener: (_: string, l: (typeof pageListeners)[number]) => pageListeners.push(l),
    parent: undefined as unknown,
  };
  const parent = {
    postMessage(data: unknown) {
      posted.push(data);
      const msg = readSettingsPageMessage({ origin: "null", source: pageWindow, data: structuredClone(data) }, pageWindow);
      if (msg?.type === "settings.ready") toPage(settingsPageInit("dark", "org"));
      else if (msg) void host.answer(msg).then((answer) => answer && toPage(answer));
    },
  };
  pageWindow.parent = parent;
  const g = globalThis as unknown as Record<string, unknown>;
  g.window = pageWindow;
  g.document = { getElementById: (id: string) => elements.get(id), createElement: () => new FakeElement(), documentElement: root };
  await import("../../../core/pkgmeta/testdata/settings-page/page.js" as string);
  const settle = () => new Promise((r) => setTimeout(r, 0));
  for (let i = 0; i < 5; i++) await settle();

  const inputs = elements.get("fields")!.children.map((label) => label.children[0]);
  eq(root.attrs["data-theme"], "dark", "the theme");
  eq(inputs.map((i) => [i.name, i.type, i.value, i.placeholder]), [["apiKey", "password", "", "set"], ["pageSize", "text", "50", ""]], "rendered");
  eq(elements.get("status")!.textContent, "Needs setup: apiBase", "missing");

  inputs[1].value = "lots";
  elements.get("save")!.listeners.forEach((cb) => cb());
  for (let i = 0; i < 5; i++) await settle();
  eq([elements.get("status")!.textContent, inputs[1].attrs["aria-invalid"]], ["Page size must be a number, such as 42 or 0.5", "true"], "refused at its field");

  inputs[1].value = "25";
  elements.get("save")!.listeners.forEach((cb) => cb());
  for (let i = 0; i < 5; i++) await settle();
  eq(saves.at(-1), { values: { pageSize: "25" } }, "an empty secret is not sent");
  eq(elements.get("status")!.textContent, "Saved. Needs setup: apiBase", "saved");
  elements.get("close")!.listeners.forEach((cb) => cb());
  eq(posted.at(-1), { type: "settings.close" }, "close");
  if (JSON.stringify(received).includes("s3cr3t")) throw new Error("a secret reached the page");
});
