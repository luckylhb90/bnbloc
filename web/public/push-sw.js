// bnbloc's one service worker: browser push (N4) and the installed app's
// offline shell (R14). Registered once per page load by src/lib/appWorker.ts
// in a production browser build, never in the desktop shell. Served by the
// gateway from the web root (scope "/") with Cache-Control: no-cache so a
// deploy's worker replaces the old one. (One worker per scope: a second
// worker file would replace this one, so push and caching live together.)
//
// The push message is the JSON WebPushMessage of internal/wire/webpush.go:
//   { id, type, roomId, title, body, url, path }
// `path` is the deep link's hash route ("#/room/<id>?seq=<n>[&channel=<c>]"),
// opened on this worker's own origin.
"use strict";

// ---- App shell cache (R14) -------------------------------------------------
// shellRoute decides each request (scripts/pwa.test.mjs runs it):
//   "page"    - a top-level navigation to the app root (index.html): network
//               first. The gateway serves it no-store, so a deploy is picked
//               up on the next start and the web reload notice keeps
//               comparing against the live build; the last good copy answers
//               only when the network fails (offline start).
//   "asset"   - <scope>assets/* (content-hashed file names): cache first.
//   "network" - everything else is never touched or cached: /v1/* and /api/*
//               (API calls, the SSE streams, artifact downloads), non-GET
//               requests, other origins, the worker and the reload notice's
//               own fetch of index.html (not a navigation).
// Bump SHELL_VERSION when the caching scheme changes: activate drops every
// other bnbloc cache.
var SHELL_VERSION = "v2";
var SHELL_CACHE = "bnbloc-shell-" + SHELL_VERSION;
var CACHE_PREFIX = "bnbloc-";

function shellRoute(req, scopeUrl) {
  if (req.method !== "GET") return "network";
  var url = new URL(req.url);
  var scope = new URL(scopeUrl);
  if (url.origin !== scope.origin || url.pathname.indexOf(scope.pathname) !== 0) return "network";
  var rest = url.pathname.slice(scope.pathname.length);
  if (req.mode === "navigate") return rest === "" || rest === "index.html" ? "page" : "network";
  return rest.indexOf("assets/") === 0 ? "asset" : "network";
}

// shellAssets lists the assets an index.html loads directly (entry script,
// preloads, styles) as scope-relative paths.
function shellAssets(html) {
  var out = [];
  var re = /\b(?:src|href)="(?:\.?\/)?(assets\/[^"?#]+)"/g;
  var m;
  while ((m = re.exec(html))) if (out.indexOf(m[1]) < 0) out.push(m[1]);
  return out;
}

// staleCaches names the caches activate deletes: older shell versions.
function staleCaches(names) {
  return names.filter(function (n) {
    return n.indexOf(CACHE_PREFIX) === 0 && n !== SHELL_CACHE;
  });
}

function isShellPage(res) {
  return !!res && res.ok && res.type === "basic" && (res.headers.get("content-type") || "").indexOf("text/html") === 0;
}

// isAsset reports a response that may stand for a hashed file: same-origin,
// ok and not a page. A page under an asset's name (an older gateway's SPA
// fallback for a chunk it did not have) is never cached, nor served from a
// cache that kept one (docs/bugs/2026-10-28-cached-html-as-chunk.md).
function isAsset(res) {
  return !!res && res.ok && res.type === "basic" && (res.headers.get("content-type") || "").indexOf("text/html") !== 0;
}

// primeShell stores a fresh index.html. When it names a different build than
// the cached copy, the previous build's assets are dropped and the new
// build's entry assets fetched, so an offline start finds a complete shell
// and the cache never accumulates old builds.
function primeShell(fresh) {
  var scope = self.registration.scope;
  return caches.open(SHELL_CACHE).then(function (cache) {
    return Promise.all([
      fresh.clone().text(),
      cache.match(scope).then(function (r) {
        return r ? r.text() : "";
      }),
    ]).then(function (texts) {
      if (texts[0] === texts[1]) return undefined;
      var wanted = shellAssets(texts[0]).map(function (p) {
        return new URL(p, scope).href;
      });
      return cache
        .put(scope, fresh)
        .then(function () {
          return cache.keys();
        })
        .then(function (keys) {
          return Promise.all(
            keys
              .filter(function (k) {
                return k.url.indexOf(scope + "assets/") === 0 && wanted.indexOf(k.url) < 0;
              })
              .map(function (k) {
                return cache.delete(k);
              }),
          );
        })
        .then(function () {
          return Promise.all(
            wanted.map(function (u) {
              return cache.match(u).then(function (hit) {
                if (hit && isAsset(hit)) return undefined;
                // Mid-deploy misses are skipped; the page fetches them later.
                return fetch(u).then(function (res) {
                  return isAsset(res) ? cache.put(u, res) : undefined;
                }).catch(function () {});
              });
            }),
          );
        });
    });
  });
}

function pageResponse(event) {
  return fetch(event.request).then(
    function (res) {
      if (isShellPage(res)) event.waitUntil(primeShell(res.clone()).catch(function () {}));
      return res;
    },
    function (err) {
      return caches
        .open(SHELL_CACHE)
        .then(function (cache) {
          return cache.match(self.registration.scope);
        })
        .then(function (hit) {
          return hit || Promise.reject(err);
        });
    },
  );
}

function assetResponse(req) {
  return caches.open(SHELL_CACHE).then(function (cache) {
    return cache.match(req).then(function (hit) {
      if (hit && isAsset(hit)) return hit;
      if (hit) cache.delete(req).catch(function () {});
      return fetch(req).then(function (res) {
        if (isAsset(res)) cache.put(req, res.clone()).catch(function () {});
        return res;
      });
    });
  });
}

self.addEventListener("install", function (event) {
  self.skipWaiting();
  // Prime the shell now: the page that registered this worker loaded its
  // assets before the worker could see them.
  event.waitUntil(
    fetch(self.registration.scope, { cache: "no-store" })
      .then(function (res) {
        return isShellPage(res) ? primeShell(res) : undefined;
      })
      .catch(function () {}),
  );
});

self.addEventListener("activate", function (event) {
  event.waitUntil(
    caches
      .keys()
      .then(function (names) {
        return Promise.all(staleCaches(names).map(function (n) {
          return caches.delete(n);
        }));
      })
      .then(function () {
        return self.clients.claim();
      }),
  );
});

self.addEventListener("fetch", function (event) {
  var route = shellRoute(event.request, self.registration.scope);
  if (route === "page") event.respondWith(pageResponse(event));
  else if (route === "asset") event.respondWith(assetResponse(event.request));
  // "network": no respondWith, so the browser handles it as if there were
  // no worker (SSE streams stay direct).
});

self.addEventListener("push", function (event) {
  var msg = {};
  try {
    msg = event.data ? event.data.json() : {};
  } catch (e) {
    msg = { title: "bnbloc", body: event.data ? event.data.text() : "" };
  }
  var title = msg.title || "bnbloc";
  event.waitUntil(
    self.registration.showNotification(title, {
      body: msg.body || "",
      // One notification per event: a retried delivery replaces the first.
      tag: msg.id || undefined,
      data: { path: msg.path || "#/" },
    }),
  );
});

self.addEventListener("notificationclick", function (event) {
  event.notification.close();
  var path = (event.notification.data && event.notification.data.path) || "#/";
  var target = new URL(path, self.registration.scope).href;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(function (list) {
      for (var i = 0; i < list.length; i++) {
        var client = list[i];
        if (new URL(client.url).origin !== self.location.origin) continue;
        // An open app tab navigates in place (the page applies the hash
        // route); focusing it brings it forward.
        client.postMessage({ type: "bnbloc.push.open", path: path });
        return client.focus();
      }
      return self.clients.openWindow(target);
    }),
  );
});
