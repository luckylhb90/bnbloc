// Installable web app (R14): the service worker's cache routing runs in a vm
// context exactly as shipped (public/push-sw.js), and the manifest, icons and
// index.html tags are checked against each other and the theme tokens.
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");
const WORKER = read("../public/push-sw.js");
const SCOPE = "https://bnbloc.example.com/";

function loadWorker() {
  const listeners = new Map();
  const self = {
    addEventListener: (type, fn) => listeners.set(type, fn),
    registration: { scope: SCOPE },
    location: { origin: new URL(SCOPE).origin },
  };
  const context = vm.createContext({ self, URL, Promise });
  vm.runInContext(WORKER, context);
  return { context, listeners };
}

test("the worker handles install, activate, fetch, push and notification clicks", () => {
  const { listeners } = loadWorker();
  for (const t of ["install", "activate", "fetch", "push", "notificationclick"]) {
    assert.equal(typeof listeners.get(t), "function", t);
  }
});

test("shellRoute: only the app page and hashed assets are cached", () => {
  const { context } = loadWorker();
  const route = (url, { method = "GET", mode = "cors", scope = SCOPE } = {}) =>
    context.shellRoute({ url, method, mode }, scope);

  // The installed app's start: network first, cached copy offline.
  assert.equal(route(SCOPE, { mode: "navigate" }), "page");
  assert.equal(route(SCOPE + "index.html", { mode: "navigate" }), "page");
  assert.equal(route(SCOPE + "?source=pwa", { mode: "navigate" }), "page");
  // Hashed bundle files: cache first.
  assert.equal(route(SCOPE + "assets/index-AbC123.js"), "asset");
  assert.equal(route(SCOPE + "assets/index-AbC123.css", { mode: "no-cors" }), "asset");

  // Never cached: API calls, SSE streams, downloads, uploads.
  assert.equal(route(SCOPE + "v1/rooms/r1/events"), "network");
  assert.equal(route(SCOPE + "v1/rooms"), "network");
  assert.equal(route(SCOPE + "api/anything"), "network");
  assert.equal(route(SCOPE + "v1/artifacts/abc", { mode: "navigate" }), "network");
  assert.equal(route(SCOPE + "assets/x.js", { method: "POST" }), "network");
  // The reload notice fetches index.html itself (not a navigation): live.
  assert.equal(route(SCOPE, { mode: "cors" }), "network");
  // Other pages, other origins, the worker, the manifest.
  assert.equal(route(SCOPE + "artifact-runner.html", { mode: "navigate" }), "network");
  assert.equal(route("https://cdn.example.net/assets/x.js"), "network");
  assert.equal(route(SCOPE + "push-sw.js"), "network");
  assert.equal(route(SCOPE + "manifest.webmanifest"), "network");

  // A gateway under a path prefix scopes everything to it.
  const sub = "https://corp.example.com/bnbloc/";
  assert.equal(route(sub, { mode: "navigate", scope: sub }), "page");
  assert.equal(route(sub + "assets/a-1.js", { scope: sub }), "asset");
  assert.equal(route("https://corp.example.com/assets/a-1.js", { scope: sub }), "network");
});

test("shellAssets lists the entry assets of a built index.html", () => {
  const { context } = loadWorker();
  const html = `<!doctype html><html><head>
    <link rel="manifest" href="/manifest.webmanifest" />
    <script type="module" crossorigin src="./assets/index-CaezWxTa.js"></script>
    <link rel="modulepreload" crossorigin href="./assets/vendor-x1.js">
    <link rel="stylesheet" crossorigin href="./assets/index-BZvjuMah.css">
    <script type="module" crossorigin src="./assets/index-CaezWxTa.js"></script>
  </head></html>`;
  assert.deepEqual(
    JSON.parse(JSON.stringify(context.shellAssets(html))),
    ["assets/index-CaezWxTa.js", "assets/vendor-x1.js", "assets/index-BZvjuMah.css"],
  );
  assert.deepEqual(JSON.parse(JSON.stringify(context.shellAssets("404 page not found"))), []);
});

// A page under an asset's name (an older pod's SPA fallback for a chunk it
// does not have, during a rolling deploy) is never cached, and one a cache
// kept from before is dropped and fetched again
// (docs/bugs/2026-10-28-cached-html-as-chunk.md).
test("assets: a page under an asset's name is never cached or served from cache", async () => {
  const resp = (type, body) => ({
    ok: true,
    type: "basic",
    body,
    headers: { get: (h) => (h.toLowerCase() === "content-type" ? type : null) },
    clone() {
      return this;
    },
  });
  const store = new Map();
  const deleted = [];
  const cache = {
    match: async (r) => store.get(typeof r === "string" ? r : r.url),
    put: async (r, res) => void store.set(typeof r === "string" ? r : r.url, res),
    delete: async (r) => void deleted.push(typeof r === "string" ? r : r.url),
  };
  let network = resp("text/html; charset=utf-8", "<!doctype html>");
  const self = { addEventListener: () => {}, registration: { scope: SCOPE }, location: { origin: new URL(SCOPE).origin } };
  const context = vm.createContext({
    self,
    URL,
    Promise,
    caches: { open: async () => cache },
    fetch: async () => network,
  });
  vm.runInContext(WORKER, context);
  const req = { url: SCOPE + "assets/Room-Ab12.js", method: "GET", mode: "cors" };

  // The network answers a page: it is passed on, not stored.
  assert.equal((await context.assetResponse(req)).body, "<!doctype html>");
  assert.equal(store.size, 0);
  // The real chunk is stored and then served from the cache.
  network = resp("text/javascript; charset=utf-8", "export{}");
  assert.equal((await context.assetResponse(req)).body, "export{}");
  network = resp("text/html", "<!doctype html>");
  assert.equal((await context.assetResponse(req)).body, "export{}");
  // A page a cache kept from before is dropped and the chunk fetched again.
  store.set(req.url, resp("text/html", "<!doctype html>"));
  network = resp("text/javascript", "export{}");
  assert.equal((await context.assetResponse(req)).body, "export{}");
  assert.deepEqual(deleted, [req.url]);
  assert.equal(context.SHELL_VERSION, "v2");
});

test("activate drops older shell caches and nothing else", () => {
  const { context } = loadWorker();
  const stale = context.staleCaches(["bnbloc-shell-v0", context.SHELL_CACHE, "other-app-cache"]);
  assert.deepEqual(JSON.parse(JSON.stringify(stale)), ["bnbloc-shell-v0"]);
});

// hsl(H S% L%) -> #rrggbb
function hslToHex(h, s, l) {
  s /= 100;
  l /= 100;
  const k = (n) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = (n) => l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
  return "#" + [f(0), f(8), f(4)].map((x) => Math.round(x * 255).toString(16).padStart(2, "0")).join("");
}

function pngSize(path) {
  const b = readFileSync(new URL(path, import.meta.url));
  assert.equal(b.toString("ascii", 1, 4), "PNG", path);
  return [b.readUInt32BE(16), b.readUInt32BE(20)];
}

test("the manifest is installable and matches the theme tokens", () => {
  const m = JSON.parse(read("../public/manifest.webmanifest"));
  assert.equal(m.display, "standalone");
  assert.equal(m.start_url, "./");
  assert.equal(m.scope, "./");
  assert.ok(m.name && m.short_name);

  const bg = /--color-bg:\s*hsl\((\d+)\s+(\d+)%\s+(\d+)%\)/.exec(read("../src/styles.css"));
  assert.ok(bg, "--color-bg token");
  const hex = hslToHex(Number(bg[1]), Number(bg[2]), Number(bg[3]));
  assert.equal(m.background_color, hex);
  assert.equal(m.theme_color, hex);

  const sizes = (purpose) => m.icons.filter((i) => i.purpose === purpose).map((i) => i.sizes);
  assert.ok(sizes("any").includes("192x192") && sizes("any").includes("512x512"), "any icons");
  assert.ok(sizes("maskable").includes("512x512"), "maskable icon");
  for (const icon of m.icons) {
    const [w, h] = pngSize("../public/" + icon.src);
    assert.equal(`${w}x${h}`, icon.sizes, icon.src);
  }
});

test("index.html links the manifest, theme color and Home Screen icon", () => {
  const html = read("../index.html");
  const m = JSON.parse(read("../public/manifest.webmanifest"));
  assert.match(html, /<link rel="manifest" href="\/manifest\.webmanifest"/);
  assert.match(html, new RegExp(`<meta name="theme-color" content="${m.theme_color}"`));
  const touch = /<link rel="apple-touch-icon" href="\/([^"]+)"/.exec(html);
  assert.ok(touch && existsSync(new URL("../public/" + touch[1], import.meta.url)), "apple-touch-icon file");
  assert.deepEqual(pngSize("../public/" + touch[1]), [180, 180]);
  assert.match(html, /apple-mobile-web-app-capable/);
});
