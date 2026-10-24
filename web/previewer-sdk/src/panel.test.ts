import type { InboundEvent } from "./index.ts";
import { createPanelClient, PanelRefused } from "./panel.ts";
import { PANEL_BODY_LIMIT, PANEL_PATH_LIMIT, PANEL_PROTOCOL, readPanelHostMessage, readPanelMessage } from "./panelProtocol.ts";

// Local runner, as settings.test.ts.
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
  const client = createPanelClient({ parent, hostOrigin, addMessageListener: (l) => (listener = l) });
  const deliver = (data: unknown, origin = HOST, source: unknown = parent) => listener?.({ origin, source, data });
  return { client, posted, deliver };
}

await test("ready once; inert without a host origin", () => {
  const h = harness();
  h.client.announceReady();
  h.client.announceReady();
  eq(h.posted, [{ type: "panel.ready" }], "one ready");
  const inert = harness("");
  inert.client.announceReady();
  eq([inert.client.active, inert.posted], [false, []], "inert");
});

await test("init reaches handlers registered before and after it", () => {
  const h = harness();
  const seen: unknown[] = [];
  h.client.onInit((init) => seen.push(init));
  h.deliver({ type: "panel.init", protocol: PANEL_PROTOCOL, theme: "dark" });
  h.client.onInit((init) => seen.push(init));
  eq(seen, [{ theme: "dark" }, { theme: "dark" }], "init twice");
});

await test("a call is answered by id, with the plugin's status whatever it is", async () => {
  const h = harness();
  const ok = h.client.call("GET", "/v1/state");
  const conflict = h.client.call("POST", "/v1/agents/x/install", { force: true });
  eq(h.posted, [
    { type: "panel.call", id: 1, method: "GET", path: "/v1/state" },
    { type: "panel.call", id: 2, method: "POST", path: "/v1/agents/x/install", body: { force: true } },
  ], "two calls");
  h.deliver({ type: "panel.result", id: 2, status: 409, body: { error: "busy" } });
  h.deliver({ type: "panel.result", id: 1, status: 200, body: { a: 1 } });
  eq(await ok, { status: 200, body: { a: 1 } }, "first");
  eq(await conflict, { status: 409, body: { error: "busy" } }, "a 409 is an answer, not an exception");
});

await test("a refusal rejects with its reason", async () => {
  const h = harness();
  const call = h.client.call("GET", "/v1/state");
  h.deliver({ type: "panel.refused", id: 1, reason: "the plugin is not running" });
  try {
    await call;
    throw new Error("resolved");
  } catch (e) {
    eq([e instanceof PanelRefused, (e as Error).message], [true, "the plugin is not running"], "refused");
  }
});

await test("other senders and other origins are ignored", async () => {
  const h = harness();
  const call = h.client.call("GET", "/v1/state");
  h.deliver({ type: "panel.result", id: 1, status: 200, body: {} }, "https://evil.example");
  h.deliver({ type: "panel.result", id: 1, status: 200, body: {} }, HOST, {});
  h.deliver({ type: "panel.result", id: 1, status: "200", body: {} });
  h.deliver({ type: "panel.result", id: 1, status: 200, body: { ok: true } });
  eq(await call, { status: 200, body: { ok: true } }, "only the real answer");
});

await test("a call outside the protocol's bounds is not posted", async () => {
  const h = harness();
  for (const [path, body] of [["/v1/" + "a".repeat(PANEL_PATH_LIMIT), undefined], ["/v1/x", "x".repeat(PANEL_BODY_LIMIT)]] as const) {
    try {
      await h.client.call("POST", path, body);
      throw new Error("posted");
    } catch (e) {
      if (!(e as Error).message.includes("bounds")) throw e;
    }
  }
  eq(h.posted, [], "nothing posted");
});

await test("the frame -> host validator takes only the known, well-formed messages", () => {
  eq(readPanelMessage({ type: "panel.ready" }), { type: "panel.ready" }, "ready");
  eq(readPanelMessage({ type: "panel.close" }), { type: "panel.close" }, "close");
  eq(readPanelMessage({ type: "panel.call", id: 3, method: "GET", path: "/v1/state" }), { type: "panel.call", id: 3, method: "GET", path: "/v1/state" }, "call");
  for (const bad of [
    null, [], "panel.ready", { type: "panel.exec" },
    { type: "panel.call", id: -1, method: "GET", path: "/v1/x" },
    { type: "panel.call", id: 1.5, method: "GET", path: "/v1/x" },
    { type: "panel.call", id: 1, method: "PATCH", path: "/v1/x" },
    { type: "panel.call", id: 1, method: "GET", path: 7 },
    { type: "panel.call", id: 1, method: "GET", path: "/" + "a".repeat(PANEL_PATH_LIMIT) },
    { type: "panel.call", id: 1, method: "POST", path: "/v1/x", body: "x".repeat(PANEL_BODY_LIMIT + 1) },
  ]) {
    eq(readPanelMessage(bad), null, `refused ${JSON.stringify(bad)?.slice(0, 40)}`);
  }
});

await test("the host -> frame validator", () => {
  eq(readPanelHostMessage({ type: "panel.init", protocol: 2, theme: "dark" }), null, "another protocol");
  eq(readPanelHostMessage({ type: "panel.init", protocol: PANEL_PROTOCOL, theme: "blue" }), null, "an unknown theme");
  eq(readPanelHostMessage({ type: "panel.refused", id: 1, reason: "x".repeat(501) }), null, "a long reason");
  eq(readPanelHostMessage({ type: "panel.result", id: 1, status: 200, body: null }), { type: "panel.result", id: 1, status: 200, body: null }, "a null body");
});
