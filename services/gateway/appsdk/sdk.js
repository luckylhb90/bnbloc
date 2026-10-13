/*
 * bnbloc app SDK: an interactive app's data, from inside the app.
 *
 * Served by the gateway at ~app/sdk.js beside every published site and
 * versioned with the gateway (docs/prd/2026-10-12-interactive-apps.md §4.1).
 * Load it with a classic script tag, relative to the page:
 *
 *   <script src="~app/sdk.js"></script>
 *
 * It defines `bnblocApp` (and `app`, unless the page defines its own) on
 * window. Types: ~app/sdk.d.ts.
 *
 *   const votes = app.collection("votes");
 *   const me = await app.me();                     // {id, name?}
 *   await votes.put(me.id, {choice: "B"}, {ifAbsent: true});   // one per person
 *   const {docs, next} = await votes.find({choice: {$eq: "B"}}, {limit: 50});
 *   const stop = app.subscribe(change => refresh());  // {appId, collection, id, version}
 *
 * Every call is relative to the capability the page was opened with (a
 * member's site ticket or a public share link), so the page handles no
 * tokens of its own and the SDK uses no storage: the frame's origin is
 * opaque. A public link's page additionally needs a visitor token, which it
 * gets from the share page hosting it (the visitor handshake below).
 *
 * A failed call rejects with an Error carrying `status` (the HTTP status:
 * 404 not found or not readable, 409 a version or ifAbsent conflict, 413 too
 * large, 429 slow down) and `code`.
 *
 * Visitor handshake (window.postMessage; neither origin can be named, so
 * each side checks the message's source window):
 *
 *   frame  -> parent: {type: "bnbloc.app.visitor.request", v: 1}
 *   parent -> frame:  {type: "bnbloc.app.visitor", v: 1, token: "<visitor token>"}
 *
 * The SDK asks once at load and again when a call is refused for a missing
 * or invalid visitor token (then it retries that call once). It accepts a
 * grant only from window.parent; the parent may send one unasked.
 */
(function (global) {
  "use strict";

  // Wire constants (internal/wire/apps.go, limits.go; pinned by the
  // gateway's tests).
  var VISITOR_HEADER = "X-Bnbloc-Visitor";
  var VISITOR_REQUEST = "bnbloc.app.visitor.request";
  var VISITOR_GRANT = "bnbloc.app.visitor";
  var HANDSHAKE_VERSION = 1;
  var VISITOR_REQUIRED = "visitor_token_required";
  var VISITOR_INVALID = "visitor_token_invalid";
  var CHANGE_EVENT = "app_changed";
  var MAX_DOC_BYTES = 65536;

  var HANDSHAKE_TIMEOUT_MS = 5000;
  var RECONNECT_MIN_MS = 1000;
  var RECONNECT_MAX_MS = 30000;

  // The API base is this script's directory (".../~app/"); a module load
  // has no currentScript and resolves against the page.
  var script = global.document && global.document.currentScript;
  var base = script && script.src
    ? script.src.replace(/[^/]*$/, "")
    : new URL("~app/", global.document ? global.document.baseURI : global.location.href).href;

  function appError(status, code, message) {
    var e = new Error(message);
    e.name = "AppError";
    e.status = status;
    e.code = code || "";
    return e;
  }

  // ---- the visitor token (public links only) ----

  var inFrame = !!global.parent && global.parent !== global;
  var visitorToken = "";
  var waiters = [];

  global.addEventListener("message", function (ev) {
    if (!inFrame || ev.source !== global.parent) return;
    var m = ev.data;
    if (!m || m.type !== VISITOR_GRANT || m.v !== HANDSHAKE_VERSION || typeof m.token !== "string" || !m.token) return;
    visitorToken = m.token;
    var ready = waiters;
    waiters = [];
    ready.forEach(function (fn) { fn(); });
  });

  function askVisitor() {
    if (inFrame) global.parent.postMessage({ type: VISITOR_REQUEST, v: HANDSHAKE_VERSION }, "*");
  }

  // visitor resolves once a token other than stale is held.
  function visitor(stale) {
    if (visitorToken && visitorToken !== stale) return Promise.resolve();
    if (!inFrame) return Promise.reject(appError(401, VISITOR_REQUIRED, "this app needs the share page that hosts it"));
    return new Promise(function (resolve, reject) {
      var timer = setTimeout(function () {
        waiters = waiters.filter(function (fn) { return fn !== done; });
        reject(appError(401, VISITOR_REQUIRED, "the share page gave this app no visitor token"));
      }, HANDSHAKE_TIMEOUT_MS);
      function done() { clearTimeout(timer); resolve(); }
      waiters.push(done);
      askVisitor();
    });
  }

  askVisitor();

  // ---- requests ----

  // send makes one request with the current visitor token; a refusal for
  // a missing or invalid token fetches a fresh one and retries once.
  function send(path, init) {
    function attempt() {
      var headers = {};
      var extra = init.headers || {};
      for (var k in extra) if (Object.prototype.hasOwnProperty.call(extra, k)) headers[k] = extra[k];
      if (visitorToken) headers[VISITOR_HEADER] = visitorToken;
      return fetch(base + path, {
        method: init.method || "GET",
        headers: headers,
        body: init.body,
        signal: init.signal,
        credentials: "omit",
        cache: "no-store",
        referrerPolicy: "no-referrer",
      });
    }
    var used = visitorToken;
    return attempt().then(function (resp) {
      if (resp.status !== 401) return resp;
      return resp.clone().json().catch(function () { return {}; }).then(function (body) {
        if (!body || (body.code !== VISITOR_REQUIRED && body.code !== VISITOR_INVALID)) return resp;
        if (visitorToken === used) visitorToken = "";
        return visitor(used).then(attempt);
      });
    });
  }

  function parse(resp) {
    if (resp.status === 204) return null;
    return resp.text().then(function (text) {
      var data = null;
      try { data = text ? JSON.parse(text) : null; } catch (e) { data = null; }
      if (!resp.ok) {
        throw appError(resp.status, data && data.code, (data && data.error) || "request failed (" + resp.status + ")");
      }
      return data;
    });
  }

  function call(method, path, body, headers) {
    var init = { method: method, headers: headers || {} };
    if (body !== undefined) {
      init.body = JSON.stringify(body);
      init.headers["Content-Type"] = "application/json";
    }
    return send(path, init).then(parse);
  }

  function checkDoc(doc) {
    if (doc === null || typeof doc !== "object" || Array.isArray(doc)) {
      return appError(400, "", "a document is a JSON object");
    }
    if (new TextEncoder().encode(JSON.stringify(doc)).length > MAX_DOC_BYTES) {
      return appError(413, "", "a document is at most " + MAX_DOC_BYTES + " bytes");
    }
    return null;
  }

  function conditional(opts, absent) {
    var h = {};
    if (opts && opts.ifVersion) h["If-Match"] = String(opts.ifVersion);
    if (absent && opts && opts.ifAbsent) h["If-None-Match"] = "*";
    return h;
  }

  var enc = encodeURIComponent;

  function Collection(name) {
    this.name = name;
    this.path = "c/" + enc(name);
  }

  Collection.prototype.insert = function (doc, opts) {
    var bad = checkDoc(doc);
    if (bad) return Promise.reject(bad);
    return call("POST", this.path, { doc: doc, public: !!(opts && opts.public) });
  };

  Collection.prototype.find = function (filter, opts) {
    var q = [];
    if (filter && Object.keys(filter).length) q.push("filter=" + enc(JSON.stringify(filter)));
    if (opts && opts.sort) q.push("sort=" + enc(opts.sort));
    if (opts && opts.limit) q.push("limit=" + enc(String(opts.limit)));
    if (opts && opts.after) q.push("after=" + enc(opts.after));
    return call("GET", this.path + (q.length ? "?" + q.join("&") : ""));
  };

  Collection.prototype.get = function (id) {
    return call("GET", this.path + "/" + enc(id));
  };

  Collection.prototype.put = function (id, doc, opts) {
    var bad = checkDoc(doc);
    if (bad) return Promise.reject(bad);
    var body = { doc: doc };
    if (opts && typeof opts.public === "boolean") body.public = opts.public;
    return call("PUT", this.path + "/" + enc(id), body, conditional(opts, true));
  };

  Collection.prototype.patch = function (id, ops, opts) {
    ops = ops || {};
    var body = {};
    if (ops.merge !== undefined) body.merge = ops.merge;
    if (ops.$inc !== undefined) body.$inc = ops.$inc;
    if (typeof ops.public === "boolean") body.public = ops.public;
    return call("PATCH", this.path + "/" + enc(id), body, conditional(opts, false));
  };

  Collection.prototype.remove = function (id, opts) {
    return call("DELETE", this.path + "/" + enc(id), undefined, conditional(opts, false)).then(function () {});
  };

  // ---- the change feed ----

  // readEvents reads a text/event-stream body, calling fn with each
  // app_changed event's data.
  function readEvents(body, fn) {
    var reader = body.getReader();
    var decoder = new TextDecoder();
    var buf = "";
    var event = "";
    var data = "";
    function line(raw) {
      var l = raw.charAt(raw.length - 1) === "\r" ? raw.slice(0, -1) : raw;
      if (l === "") {
        if (event === CHANGE_EVENT && data) {
          var change = null;
          try { change = JSON.parse(data); } catch (e) { change = null; }
          if (change) {
            try { fn(change); } catch (e) { setTimeout(function () { throw e; }); }
          }
        }
        event = "";
        data = "";
        return;
      }
      if (l.charAt(0) === ":") return;
      var i = l.indexOf(":");
      var field = i < 0 ? l : l.slice(0, i);
      var value = i < 0 ? "" : l.slice(i + 1).replace(/^ /, "");
      if (field === "event") event = value;
      else if (field === "data") data = data ? data + "\n" + value : value;
    }
    function pump() {
      return reader.read().then(function (r) {
        if (r.done) return;
        buf += decoder.decode(r.value, { stream: true });
        var lines = buf.split("\n");
        buf = lines.pop();
        lines.forEach(line);
        return pump();
      });
    }
    return pump();
  }

  // subscribe calls fn with each change of the app's data ({appId,
  // collection, id, version}; version 0: deleted; no collection: all data
  // was deleted) until the returned function is called. Changes are live
  // only: after a disconnection the SDK reconnects, and changes made while
  // it was away are not replayed, so re-read what the page shows.
  function subscribe(fn) {
    var stopped = false;
    var ctrl = null;
    var delay = RECONNECT_MIN_MS;
    function connect() {
      if (stopped) return;
      ctrl = new AbortController();
      send("events", { signal: ctrl.signal, headers: { Accept: "text/event-stream" } })
        .then(function (resp) {
          if (!resp.ok || !resp.body) throw appError(resp.status, "", "change feed refused (" + resp.status + ")");
          delay = RECONNECT_MIN_MS;
          return readEvents(resp.body, fn);
        })
        .catch(function () {})
        .then(function () {
          if (stopped) return;
          setTimeout(connect, delay);
          delay = Math.min(delay * 2, RECONNECT_MAX_MS);
        });
    }
    connect();
    return function unsubscribe() {
      stopped = true;
      if (ctrl) ctrl.abort();
    };
  }

  var app = {
    collection: function (name) { return new Collection(name); },
    me: function () { return call("GET", "me"); },
    subscribe: subscribe,
  };

  global.bnblocApp = app;
  if (!Object.prototype.hasOwnProperty.call(global, "app")) global.app = app;
})(typeof window !== "undefined" ? window : globalThis);
