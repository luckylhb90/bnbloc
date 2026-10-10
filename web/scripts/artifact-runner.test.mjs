// Guest-side checks of the live-artifact runner (public/artifact-runner.js):
// the script runs in a vm context with a stubbed window/document, so the
// accept/refuse rules are exercised exactly as shipped.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const RUNNER = readFileSync(new URL("../public/artifact-runner.js", import.meta.url), "utf8");
const HOST = "tauri://localhost";

function boot({ origin = "null", framed = true, hash = `#host=${encodeURIComponent(HOST)}` } = {}) {
  const listeners = new Map();
  const written = [];
  const posted = [];
  // Messages are built in the vm realm: store a plain copy so deepEqual does
  // not trip over the other realm's Object prototype.
  const parent = { postMessage: (data, target) => posted.push({ data: JSON.parse(JSON.stringify(data)), target }) };
  const window = {
    addEventListener: (type, fn) => listeners.set(type, fn),
    removeEventListener: (type, fn) => {
      if (listeners.get(type) === fn) listeners.delete(type);
    },
  };
  window.parent = framed ? parent : window;
  const docListeners = new Map();
  const document = {
    body: { textContent: "" },
    addEventListener: (type, fn) => docListeners.set(type, fn),
    open: () => written.push("<open>"),
    write: (html) => written.push(html),
    close: () => written.push("<close>"),
  };
  const context = {
    window,
    self: { origin },
    document,
    location: { hash },
    URLSearchParams,
  };
  vm.runInNewContext(RUNNER, context);
  const send = (ev) => listeners.get("message")?.(ev);
  return { parent, posted, written, send, listeners, docListeners, document };
}

test("a sandboxed, framed runner pings its parent once", () => {
  const r = boot();
  assert.deepEqual(r.posted, [{ data: { type: "artifact-runner-ready" }, target: "*" }]);
  assert.ok(r.listeners.has("message"));
});

test("it writes exactly one document, only from the parent at the named host origin", () => {
  const r = boot();
  const load = { type: "artifact-load", html: "<p>hi</p>" };
  r.send({ source: {}, origin: HOST, data: load });
  r.send({ source: r.parent, origin: "https://evil.example", data: load });
  r.send({ source: r.parent, origin: HOST, data: { type: "artifact-load", html: 42 } });
  r.send({ source: r.parent, origin: HOST, data: { type: "other", html: "x" } });
  assert.deepEqual(r.written, [], "foreign source/origin and malformed messages are ignored");

  r.send({ source: r.parent, origin: HOST, data: load });
  assert.deepEqual(r.written, ["<open>", "<p>hi</p>", "<close>"]);
  assert.ok(!r.listeners.has("message"), "the hand-off listener is gone after the one document");
});

for (const [name, opts] of [
  ["not sandboxed (real origin)", { origin: "https://gw.example" }],
  ["opened top-level", { framed: false }],
  ["no host origin in the fragment", { hash: "" }],
]) {
  test(`refuses to run when ${name}`, () => {
    const r = boot(opts);
    assert.deepEqual(r.posted, []);
    assert.ok(!r.listeners.has("message"), "no hand-off listener");
    r.docListeners.get("DOMContentLoaded")?.();
    assert.match(r.document.body.textContent, /only runs inside a sandboxed artifact preview/);
  });
}
