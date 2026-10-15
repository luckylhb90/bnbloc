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
    FormData,
    Blob,
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

test("files upload as one multipart part, read back as a blob URL, refuse oversized files before sending", async () => {
  const blobs = [];
  const sdk = boot((req) =>
    req.url.endsWith("/content")
      ? new Response(new Uint8Array([1, 2, 3]), { status: 200 })
      : jsonResponse(201, { id: "f1", name: "a.png", type: "image/png", size: 3 }),
  );
  const file = new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" });
  file.name = "a.png";
  const up = plain(await sdk.app.files.upload(file, { public: true }));
  assert.equal(up.id, "f1");
  const [post] = sdk.requests;
  assert.equal(post.method, "POST");
  assert.equal(post.url, BASE + "files?public=1");
  assert.ok(post.body instanceof FormData && post.body.get("file").size === 3);
  assert.equal(post.headers["Content-Type"], undefined, "the browser sets the multipart boundary");
  globalThis.URL.createObjectURL = (b) => (blobs.push(b), "blob:x");
  assert.equal(await sdk.app.files.url("f 1"), "blob:x");
  assert.equal(sdk.requests[1].url, BASE + "files/f%201/content");
  await assert.rejects(sdk.app.files.upload({ size: 26214401, name: "big.mp4" }), (e) => e.status === 413);
  assert.equal(sdk.requests.length, 2);
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

// Demo mode (the gallery's "Try demo"): the same file with its mode
// argument "demo", as the gateway serves it under a demo ticket
// (appsdk.DemoJS, pinned by its Go test).
const LIVE_MODE = ', "live");';
const DEMO_SDK = SDK.replace(LIVE_MODE, ', "demo");');

test("the SDK passes its mode once, at its end", () => {
  assert.equal(SDK.split(LIVE_MODE).length, 2);
  assert.ok(SDK.trimEnd().endsWith(LIVE_MODE));
});

// bootDemo loads the demo SDK in a frame whose every way out throws: a
// request, a message to the parent, a message listener.
function bootDemo() {
  const out = [];
  const way = (name) => (...args) => {
    out.push(name);
    throw new Error(`demo mode used ${name}(${args.length} args)`);
  };
  const window = {
    addEventListener: way("addEventListener"),
    parent: { postMessage: way("postMessage") },
    document: { currentScript: { src: BASE + "sdk.js" }, baseURI: BASE },
    location: { href: BASE },
  };
  const context = { window, fetch: way("fetch"), XMLHttpRequest: way("XMLHttpRequest"), EventSource: way("EventSource"), URL, TextEncoder, Blob, setTimeout, clearTimeout, Promise };
  vm.createContext(context);
  vm.runInContext(DEMO_SDK, context);
  return { app: window.bnblocApp, out };
}

test("demo mode answers the whole data API in the page's memory and never sends a request", async () => {
  const { app, out } = bootDemo();
  const changes = [];
  const stop = app.subscribe((c) => changes.push(plain(c)));
  const votes = app.collection("votes");

  const me = plain(await app.me());
  assert.deepEqual(me, { id: "demo", name: "Demo", account: { login: "demo@example.com", email: "demo@example.com" } });
  const first = plain(await votes.insert({ choice: "A" }, { public: true }));
  assert.equal(first.choice, "A");
  assert.equal(first._by, "demo");
  assert.equal(first._version, 1);
  // One per person: ifAbsent refuses a second; ifVersion follows the version.
  const mine = plain(await votes.put(me.id, { choice: "B", n: 1 }, { ifAbsent: true }));
  await assert.rejects(votes.put(me.id, { choice: "C" }, { ifAbsent: true }), (e) => e.status === 409);
  await assert.rejects(votes.patch(me.id, { $inc: { n: 1 } }, { ifVersion: 7 }), (e) => e.status === 409);
  const patched = plain(await votes.patch(me.id, { merge: { note: "hi" }, $inc: { n: 2 } }, { ifVersion: mine._version }));
  assert.deepEqual([patched.choice, patched.n, patched.note, patched._version, patched._createdAt], ["B", 3, "hi", 2, mine._createdAt]);
  // Stamps are the server's: a document's own _id or _version is ignored.
  assert.equal(plain(await votes.put("x1", { _version: 99, _id: "y", v: 1 }))._version, 1);

  // Filters, _id order and paging as the data API has them ("demo" sorts
  // before the server ids "demo000…").
  const page = plain(await votes.find({ choice: { $in: ["A", "B"] } }, { limit: 1 }));
  assert.deepEqual(page.docs.map((d) => d._id), [me.id]);
  const rest = plain(await votes.find({ choice: { $in: ["A", "B"] } }, { limit: 1, after: page.next }));
  assert.deepEqual(rest.docs.map((d) => d._id), [first._id]);
  assert.equal(rest.next, undefined);
  assert.deepEqual(plain(await votes.find({ n: { $gte: 3 } }, { sort: "-_id" })).docs.map((d) => d._id), [me.id]);
  assert.equal(plain(await votes.get(first._id)).choice, "A");

  await votes.remove(first._id);
  await assert.rejects(votes.get(first._id), (e) => e.status === 404);
  await assert.rejects(app.collection("bad name").insert({ a: 1 }), (e) => e.status === 400);
  await assert.rejects(votes.insert({ big: "x".repeat(70000) }), (e) => e.status === 413);

  // Files stay in the page too.
  const photo = new Blob(["png"], { type: "image/png" });
  const f = plain(await app.files.upload(photo));
  assert.equal(plain(await app.files.get(f.id)).size, 3);
  await app.files.remove(f.id);
  await assert.rejects(app.files.get(f.id), (e) => e.status === 404);

  await new Promise((r) => setTimeout(r, 10));
  assert.deepEqual(
    changes.map((c) => [c.collection, c.version]),
    [["votes", 1], ["votes", 1], ["votes", 2], ["votes", 1], ["votes", 0]],
  );
  stop();
  await votes.put("x2", { v: 2 });
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(changes.length, 5, "a change reached an unsubscribed listener");
  assert.deepEqual(out, [], "demo mode reached outside the page");
});

test("demo data lives in one page: another page starts empty", async () => {
  const a = bootDemo();
  await a.app.collection("votes").insert({ choice: "A" });
  const b = bootDemo();
  assert.deepEqual(plain(await b.app.collection("votes").find()).docs, []);
});
