// Frame-side checks of the interactive apps' SDK (services/gateway/appsdk/
// sdk.js, which the gateway serves at ~app/sdk.js): the script runs in a vm
// context with a stubbed window, parent and fetch, so the visitor
// handshake, the request shapes and the change feed are exercised exactly
// as shipped.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const SDK = readFileSync(new URL("../../services/gateway/appsdk/sdk.js", import.meta.url), "utf8");
const BASE = "https://gw.example/v1/public/shares/bsh_tok/site/~app/";

const plain = (v) => JSON.parse(JSON.stringify(v));

function jsonResponse(status, body) {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

// boot loads the SDK; answer(req) returns each fetch's Response.
function boot(answer) {
  const listeners = [];
  const posted = [];
  const requests = [];
  const parent = { postMessage: (data, target) => posted.push({ data: plain(data), target }) };
  const window = {
    addEventListener: (type, fn) => type === "message" && listeners.push(fn),
    parent,
    document: { currentScript: { src: BASE + "sdk.js" }, baseURI: BASE },
    location: { href: BASE },
  };
  const context = {
    window,
    fetch: async (url, init) => {
      const req = { url, method: init.method, headers: { ...init.headers }, body: init.body, credentials: init.credentials };
      requests.push(req);
      return answer(req, requests.length);
    },
    URL,
    TextEncoder,
    TextDecoder,
    AbortController,
    Response,
    setTimeout,
    clearTimeout,
    Promise,
  };
  vm.createContext(context);
  vm.runInContext(SDK, context);
  const deliver = (source, data) => listeners.forEach((fn) => fn({ source, data }));
  return { app: window.bnblocApp, globalApp: window.app, posted, requests, parent, deliver };
}

test("asks the share page for a visitor token at load and defines the globals", () => {
  const sdk = boot(() => jsonResponse(200, {}));
  assert.deepEqual(sdk.posted, [{ data: { type: "bnbloc.app.visitor.request", v: 1 }, target: "*" }]);
  assert.ok(sdk.app && sdk.globalApp === sdk.app);
});

test("a refusal for a missing visitor token waits for the parent's grant and retries once", async () => {
  const sdk = boot((req, n) =>
    n === 1 ? jsonResponse(401, { error: "needs a visitor", code: "visitor_token_required" }) : jsonResponse(201, { _id: "d1", choice: "B" }),
  );
  const done = sdk.app.collection("votes").insert({ choice: "B" }, { public: true });
  await new Promise((r) => setTimeout(r, 10));
  // A grant from anyone but the parent is ignored.
  sdk.deliver({}, { type: "bnbloc.app.visitor", v: 1, token: "forged" });
  sdk.deliver(sdk.parent, { type: "bnbloc.app.visitor", v: 2, token: "wrong-version" });
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(sdk.requests.length, 1, "retried on a grant that is not the parent's");
  sdk.deliver(sdk.parent, { type: "bnbloc.app.visitor", v: 1, token: "vt-1" });
  assert.deepEqual(plain(await done), { _id: "d1", choice: "B" });
  assert.equal(sdk.requests.length, 2);
  const [first, retry] = sdk.requests;
  assert.equal(first.url, BASE + "c/votes");
  assert.equal(first.method, "POST");
  assert.equal(first.credentials, "omit");
  assert.equal(first.headers["X-Bnbloc-Visitor"], undefined);
  assert.equal(retry.headers["X-Bnbloc-Visitor"], "vt-1");
  assert.deepEqual(JSON.parse(retry.body), { doc: { choice: "B" }, public: true });
});

test("conditional writes and finds are spelled as the data API takes them", async () => {
  const sdk = boot(() => jsonResponse(200, { docs: [] }));
  const votes = sdk.app.collection("votes");
  await votes.put("me 1", { choice: "A" }, { ifAbsent: true });
  await votes.put("me 1", { choice: "C" }, { ifVersion: 3, public: false });
  await votes.patch("me 1", { $inc: { n: 1 } }, { ifVersion: 4 });
  await votes.find({ choice: { $in: ["A", "B"] } }, { sort: "-_id", limit: 20, after: "d9" });
  const [absent, versioned, patched, find] = sdk.requests;
  assert.equal(absent.url, BASE + "c/votes/me%201");
  assert.equal(absent.headers["If-None-Match"], "*");
  assert.deepEqual(JSON.parse(absent.body), { doc: { choice: "A" } });
  assert.equal(versioned.headers["If-Match"], "3");
  assert.deepEqual(JSON.parse(versioned.body), { doc: { choice: "C" }, public: false });
  assert.equal(patched.method, "PATCH");
  assert.deepEqual(JSON.parse(patched.body), { $inc: { n: 1 } });
  const q = new URL(find.url).searchParams;
  assert.deepEqual(JSON.parse(q.get("filter")), { choice: { $in: ["A", "B"] } });
  assert.equal(q.get("sort"), "-_id");
  assert.equal(q.get("limit"), "20");
  assert.equal(q.get("after"), "d9");
});

test("a failed call rejects with its status and code; an oversized document is refused before sending", async () => {
  const sdk = boot(() => jsonResponse(409, { error: "document exists" }));
  await assert.rejects(sdk.app.collection("votes").put("x", { a: 1 }, { ifAbsent: true }), (e) => e.status === 409 && e.message === "document exists");
  await assert.rejects(sdk.app.collection("votes").insert({ big: "x".repeat(70000) }), (e) => e.status === 413);
  assert.equal(sdk.requests.length, 1);
});

test("subscribe hands over only app_changed events, until unsubscribed", async () => {
  const frames =
    ": connected\n\n" +
    'event: chat_message\ndata: {"text":"no"}\n\n' +
    'event: app_changed\ndata: {"appId":"art_1","collection":"votes","id":"d1","version":2}\n\n' +
    'event: app_changed\r\ndata: {"appId":"art_1","collection":"votes","id":"d2","version":0}\r\n\r\n';
  let stream;
  const sdk = boot(
    () =>
      new Response(
        new ReadableStream({
          start(c) {
            stream = c;
            c.enqueue(new TextEncoder().encode(frames.slice(0, 40)));
            c.enqueue(new TextEncoder().encode(frames.slice(40)));
          },
        }),
        { status: 200, headers: { "Content-Type": "text/event-stream" } },
      ),
  );
  const got = [];
  const stop = sdk.app.subscribe((c) => got.push(plain(c)));
  await new Promise((r) => setTimeout(r, 20));
  assert.deepEqual(got, [
    { appId: "art_1", collection: "votes", id: "d1", version: 2 },
    { appId: "art_1", collection: "votes", id: "d2", version: 0 },
  ]);
  assert.equal(sdk.requests[0].url, BASE + "events");
  stop();
  stream.close();
});
