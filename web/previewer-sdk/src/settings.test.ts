import type { InboundEvent } from "./index.ts";
import { createSettingsPageClient, SettingsRefused } from "./settings.ts";
import { readHostAnswer, readPageMessage, readView, SETTING_LINES_LIMIT, SETTINGS_KEYS_LIMIT, SETTINGS_PAGE_PROTOCOL } from "./settingsProtocol.ts";

// Local runner, as index.test.ts.
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

const HOST = "https://app.example";

function harness(hostOrigin = HOST) {
  const posted: Record<string, unknown>[] = [];
  const parent = { postMessage: (message: unknown) => void posted.push(message as Record<string, unknown>) };
  let listener: ((ev: InboundEvent) => void) | null = null;
  const client = createSettingsPageClient({ parent, hostOrigin, addMessageListener: (l) => (listener = l) });
  const deliver = (data: unknown, origin = HOST, source: unknown = parent) => listener?.({ origin, source, data });
  return { client, posted, deliver };
}

const view = {
  scope: "org",
  fields: [
    { key: "apiBase", label: "API base", type: "url", required: true, set: true, value: "https://notes.example.com" },
    { key: "apiKey", label: "API key", type: "secret", required: true, set: true },
  ],
  readOnly: [{ key: "folder", label: "Folder", type: "path", scope: "member", set: false }],
  missing: [],
};

await test("ready once; inert without a host origin", () => {
  const h = harness();
  h.client.announceReady();
  h.client.announceReady();
  eq(h.posted, [{ type: "settings.ready" }], "one ready");
  const inert = harness("");
  inert.client.announceReady();
  eq([inert.client.active, inert.posted], [false, []], "inert");
});

await test("init reaches handlers registered before and after it", () => {
  const h = harness();
  const seen: unknown[] = [];
  h.client.onInit((init) => seen.push(init));
  h.deliver({ type: "settings.init", protocol: SETTINGS_PAGE_PROTOCOL, theme: "dark", scope: "org" });
  h.client.onInit((init) => seen.push(init));
  eq(seen, [{ theme: "dark", scope: "org" }, { theme: "dark", scope: "org" }], "init twice");
});

await test("get, set and check are answered by id; other senders are ignored", async () => {
  const h = harness();
  const got = h.client.get();
  eq(h.posted, [{ type: "settings.get", id: 1 }], "get posted");
  h.deliver({ type: "settings.view", id: 1, view }, "https://evil.example");
  h.deliver({ type: "settings.view", id: 1, view }, HOST, {});
  h.deliver({ type: "settings.view", id: 1, view });
  eq((await got).fields[0].value, "https://notes.example.com", "the view");

  const saved = h.client.set({ apiBase: "nope" });
  eq(h.posted[1], { type: "settings.set", values: { apiBase: "nope" }, id: 2 }, "set posted");
  h.deliver({ type: "settings.refused", id: 2, reason: "API base must be an http(s) URL", key: "apiBase" });
  const refusal = await saved.then(
    () => null,
    (e: unknown) => e,
  );
  eq(refusal instanceof SettingsRefused && [refusal.message, refusal.key], ["API base must be an http(s) URL", "apiBase"], "refused at its field");

  const checked = h.client.check();
  h.deliver({ type: "settings.checked", id: 3, supported: false });
  eq(await checked, { supported: false }, "no check yet");

  h.client.close();
  eq(h.posted.at(-1), { type: "settings.close" }, "close");
});

await test("a set that is not strings by key is refused before it is posted", async () => {
  const h = harness();
  const err = await h.client.set({ n: 5 as unknown as string }).catch((e: unknown) => e);
  eq([err instanceof Error, h.posted.length], [true, 0], "not posted");
});

await test("a secret's value never reaches the page, whatever the host sends", () => {
  const leaky = { ...view, fields: [{ key: "apiKey", label: "API key", type: "secret", required: true, set: true, value: "s3cr3t" }] };
  const msg = readHostAnswer({ type: "settings.view", id: 1, view: leaky });
  eq(msg?.type === "settings.view" && msg.view.fields[0], { key: "apiKey", label: "API key", type: "secret", required: true, set: true }, "no value");
});

await test("page messages are validated", () => {
  eq(readPageMessage({ type: "settings.set", id: 4, values: { a: "1" }, clear: ["b"] }), { type: "settings.set", id: 4, values: { a: "1" }, clear: ["b"] }, "set");
  const many = Object.fromEntries(Array.from({ length: SETTINGS_KEYS_LIMIT + 1 }, (_, i) => [`k${i}`, "v"]));
  for (const bad of [
    { type: "settings.get" },
    { type: "settings.get", id: -1 },
    { type: "settings.get", id: 1.5 },
    { type: "settings.set", id: 1, values: { a: 1 } },
    { type: "settings.set", id: 1, values: ["a"] },
    { type: "settings.set", id: 1, values: many },
    { type: "settings.set", id: 1, values: {}, clear: [1] },
    { type: "settings.set", id: 1, values: { dirs: ["a", 1] } },
    { type: "settings.set", id: 1, values: { dirs: Array.from({ length: SETTING_LINES_LIMIT + 1 }, () => "x") } },
    { type: "settings.read", id: 1 },
    "settings.ready",
  ]) {
    eq(readPageMessage(bad), null, JSON.stringify(bad));
  }
  eq(readHostAnswer({ type: "settings.init", protocol: 2, theme: "light", scope: "org" }), null, "a newer protocol");
});

await test("a lines setting's value is a list both ways", () => {
  eq(readPageMessage({ type: "settings.set", id: 5, values: { dirs: ["~/a", "~/b"], note: "x" } }), { type: "settings.set", id: 5, values: { dirs: ["~/a", "~/b"], note: "x" } }, "a list saved");
  const field = { key: "dirs", label: "Folders", type: "lines", required: false, set: true, default: ["~/b"], value: ["~/a"] };
  eq(readView({ scope: "member", fields: [field], readOnly: [], missing: [] })?.fields[0], field, "a list read");
  eq(readView({ scope: "member", fields: [{ ...field, value: [1] }], readOnly: [], missing: [] }), null, "a list of strings only");
});
