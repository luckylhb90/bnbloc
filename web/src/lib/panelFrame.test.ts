import { panelHost, panelInit, panelSrc, readPanelFrameMessage } from "./panelFrame.ts";
import { ApiError } from "./errors.ts";
import type { FrameEnv } from "./previewerFrame.ts";

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
const desktopEnv: FrameEnv = {
  convertFileSrc: (path: string, scheme: string) => `${scheme}://localhost/${path}`,
  apiUrl: (path: string) => path,
  baseURI: HOST,
  hostOrigin: HOST,
} as unknown as FrameEnv;
const browserEnv: FrameEnv = { apiUrl: (path: string) => path, baseURI: HOST, hostOrigin: HOST } as unknown as FrameEnv;

await test("a panel loads by its pin on the desktop, and nowhere else", () => {
  eq(panelSrc({ entry: "index.html", bundle: { sha256: SHA } }, desktopEnv), `bnbloc-previewer://localhost/_page/${SHA}/index.html#host=${encodeURIComponent(HOST)}`, "the desktop scheme");
  eq(panelSrc({ entry: "index.html", bundle: { sha256: SHA } }, browserEnv), null, "a browser has no sidecar to call");
  for (const entry of ["../index.html", "/index.html", "a\\b.html", ""]) {
    eq(panelSrc({ entry, bundle: { sha256: SHA } }, desktopEnv), null, `entry ${entry}`);
  }
});

await test("only the panel's own frame is heard", () => {
  const frame = {};
  const msg = { type: "panel.ready" };
  eq(readPanelFrameMessage({ origin: "null", source: frame, data: msg }, frame), msg, "the frame");
  eq(readPanelFrameMessage({ origin: "null", source: {}, data: msg }, frame), null, "another window");
  eq(readPanelFrameMessage({ origin: HOST, source: frame, data: msg }, frame), null, "a window with an origin");
  eq(readPanelFrameMessage({ origin: "null", source: frame, data: { type: "settings.get", id: 1 } }, frame), null, "another protocol's message");
});

await test("init carries the protocol and the theme", () => {
  eq(panelInit("dark"), { type: "panel.init", protocol: 1, theme: "dark" }, "init");
});

await test("a call is relayed with its status, and a call that never arrived is a refusal with its reason", async () => {
  const calls: unknown[] = [];
  const host = panelHost({
    call: async (method, path, body) => {
      calls.push([method, path, body]);
      if (path === "/v1/state") return { status: 200, body: { agents: [] } };
      if (path === "/v1/busy") return { status: 409, body: { error: "busy" } };
      if (path === "/v1/api") throw new ApiError(502, "the plugin is not running");
      throw new Error("a panel may only call paths under /v1/");
    },
  });
  eq(await host.answer({ type: "panel.call", id: 1, method: "GET", path: "/v1/state" }), { type: "panel.result", id: 1, status: 200, body: { agents: [] } }, "ok");
  eq(await host.answer({ type: "panel.call", id: 2, method: "POST", path: "/v1/busy", body: { a: 1 } }), { type: "panel.result", id: 2, status: 409, body: { error: "busy" } }, "a 409 is an answer");
  eq(await host.answer({ type: "panel.call", id: 3, method: "GET", path: "/v1/api" }), { type: "panel.refused", id: 3, reason: "the plugin is not running" }, "refused");
  eq(await host.answer({ type: "panel.call", id: 4, method: "GET", path: "/v2/x" }), { type: "panel.refused", id: 4, reason: "a panel may only call paths under /v1/" }, "the desktop's reason is the page's");
  eq(calls[1], ["POST", "/v1/busy", { a: 1 }], "the body goes through");
  eq(await host.answer({ type: "panel.ready" }), null, "ready is the frame's");
  eq(await host.answer({ type: "panel.close" }), null, "close is the frame's");
});

await test("a long reason is cut to the protocol bound", async () => {
  const host = panelHost({ call: async () => { throw new Error("x".repeat(900)); } });
  const answer = await host.answer({ type: "panel.call", id: 1, method: "GET", path: "/v1/x" });
  eq([answer?.type, [...(answer as { reason: string }).reason].length], ["panel.refused", 500], "cut");
});
