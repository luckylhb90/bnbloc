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
 *   const me = await app.me();                     // {id, name?, account?: {login, email?}}
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
 *
 * Demo mode (the gallery's "Try demo", docs/prd/2026-10-13-app-gallery.md
 * §7): the gateway serves this same file with its mode argument (the last
 * line) set to "demo" under a demo ticket. Every call is then answered by
 * an in-memory data space of this page alone (memoryBackend), with the
 * data API's shapes and rules; nothing is ever sent (no request, no change
 * feed, no visitor handshake) and nothing outlives the page.
 */
(function (global, mode) {
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
  var MAX_FIND_LIMIT = 500;
  var MAX_COLLECTION_NAME_LEN = 64;
  var MAX_DOC_ID_LEN = 128;

  var demo = mode === "demo";

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

  if (!demo) global.addEventListener("message", function (ev) {
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

  if (!demo) askVisitor();

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
    if (memory) return memory.call(method, path, body === undefined ? undefined : JSON.parse(JSON.stringify(body)), headers || {});
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
    if (memory) return memory.subscribe(fn);
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

  // ---- demo mode: an in-memory data space of this page alone ----

  // memoryBackend answers call() and subscribe() in demo mode: the data
  // API's paths, shapes and rules (stamps, ifAbsent / ifVersion
  // conditions, filters, _id order and paging, merge and $inc), held in
  // this page's memory. It has no way out: it never fetches, posts a
  // message or stores anything, and a reload starts empty.
  function memoryBackend() {
    var DEMO_ID = "demo";
    var COLLECTION = /^[A-Za-z0-9_-]+$/;
    var DOC_ID = /^[A-Za-z0-9_.:-]+$/;
    var STAMPS = { _id: 1, _by: 1, _createdAt: 1, _updatedAt: 1, _version: 1, _author: 1 };
    var data = {};
    var listeners = [];
    var seq = 0;

    function copy(v) { return v === undefined || v === null ? null : JSON.parse(JSON.stringify(v)); }
    function refuse(status, message) { return Promise.reject(appError(status, "", message)); }
    function bodyOf(doc) {
      var out = {};
      for (var k in doc) if (Object.prototype.hasOwnProperty.call(doc, k) && !STAMPS[k]) out[k] = doc[k];
      return out;
    }
    function stamped(id, body, prev) {
      var t = Date.now();
      var doc = bodyOf(body);
      doc._id = id;
      doc._by = DEMO_ID;
      doc._createdAt = prev ? prev._createdAt : t;
      doc._updatedAt = t;
      doc._version = prev ? prev._version + 1 : 1;
      return doc;
    }
    function changed(collection, id, version) {
      var change = { appId: DEMO_ID, collection: collection, id: id, version: version };
      listeners.forEach(function (fn) {
        setTimeout(function () { if (listeners.indexOf(fn) >= 0) fn(copy(change)); }, 0);
      });
    }
    function fieldOf(doc, path) {
      var v = doc;
      for (var parts = path.split("."), i = 0; i < parts.length; i++) {
        if (v === null || typeof v !== "object" || !Object.prototype.hasOwnProperty.call(v, parts[i])) return undefined;
        v = v[parts[i]];
      }
      return v;
    }
    function same(a, b) { return JSON.stringify(a) === JSON.stringify(b); }
    // compare orders two numbers or two strings; anything else never matches a range.
    function compare(a, b) {
      if (typeof a !== typeof b || (typeof a !== "number" && typeof a !== "string")) return null;
      return a < b ? -1 : a > b ? 1 : 0;
    }
    function test(v, op, arg) {
      var c;
      switch (op) {
        case "$eq": return v !== undefined && same(v, arg);
        case "$ne": return !same(v, arg);
        case "$in": return Array.isArray(arg) && arg.some(function (x) { return v !== undefined && same(v, x); });
        case "$exists": return (v !== undefined) === !!arg;
        case "$gt": c = compare(v, arg); return c !== null && c > 0;
        case "$gte": c = compare(v, arg); return c !== null && c >= 0;
        case "$lt": c = compare(v, arg); return c !== null && c < 0;
        case "$lte": c = compare(v, arg); return c !== null && c <= 0;
      }
      throw appError(400, "", "unknown filter operator " + op);
    }
    function matches(doc, filter) {
      return Object.keys(filter).every(function (path) {
        var cond = filter[path];
        var v = fieldOf(doc, path);
        var ops = cond !== null && typeof cond === "object" && !Array.isArray(cond) && Object.keys(cond).length > 0 &&
          Object.keys(cond).every(function (k) { return k.charAt(0) === "$"; });
        if (!ops) return test(v, "$eq", cond);
        return Object.keys(cond).every(function (op) { return test(v, op, cond[op]); });
      });
    }
    // mergePatch applies a JSON merge patch (null removes a field).
    function mergePatch(target, patch) {
      if (patch === null || typeof patch !== "object" || Array.isArray(patch)) return patch;
      var out = target !== null && typeof target === "object" && !Array.isArray(target) ? target : {};
      Object.keys(patch).forEach(function (k) {
        if (patch[k] === null) delete out[k];
        else out[k] = mergePatch(out[k], patch[k]);
      });
      return out;
    }
    function conflict(prev, headers) {
      if (headers["If-None-Match"] === "*" && prev) return "the document exists";
      var want = headers["If-Match"];
      if (want !== undefined && (!prev || String(prev._version) !== want)) return "the document is not at version " + want;
      return "";
    }

    function find(docs, query) {
      var filter = {};
      try { filter = query.get("filter") ? JSON.parse(query.get("filter")) : {}; } catch (e) { return refuse(400, "filter is not JSON"); }
      var desc = query.get("sort") === "-_id";
      var limit = query.get("limit") ? parseInt(query.get("limit"), 10) : 50;
      if (!(limit >= 1)) return refuse(400, "limit is a positive number");
      limit = Math.min(limit, MAX_FIND_LIMIT);
      var after = query.get("after") || "";
      var ids = Object.keys(docs).sort();
      if (desc) ids.reverse();
      var out = [];
      try {
        for (var i = 0; i < ids.length; i++) {
          if (after && (desc ? ids[i] >= after : ids[i] <= after)) continue;
          if (!matches(docs[ids[i]], filter)) continue;
          if (out.length === limit) return Promise.resolve({ docs: out, next: out[out.length - 1]._id });
          out.push(copy(docs[ids[i]]));
        }
      } catch (e) {
        return Promise.reject(e);
      }
      return Promise.resolve({ docs: out });
    }

    function call(method, path, payload, headers) {
      var url = new URL(path, "https://demo.invalid/");
      var parts = url.pathname.slice(1).split("/").map(decodeURIComponent);
      // A signed-in caller, so a page that fills in its caller's login works
      // in the demo as it does for a member.
      if (method === "GET" && parts.length === 1 && parts[0] === "me") return Promise.resolve({ id: DEMO_ID, name: "Demo", account: { login: "demo@example.com", email: "demo@example.com" } });
      if (parts[0] !== "c" || parts.length < 2 || parts.length > 3) return refuse(404, "not an app API path");
      var name = parts[1];
      var id = parts[2];
      if (!COLLECTION.test(name) || name.length > MAX_COLLECTION_NAME_LEN) return refuse(400, "invalid collection name");
      if (id !== undefined && (!DOC_ID.test(id) || id.length > MAX_DOC_ID_LEN)) return refuse(400, "invalid document id");
      var docs = data[name] || {};
      var prev = id === undefined ? undefined : docs[id];
      var why = id === undefined ? "" : conflict(prev, headers);
      var doc;
      switch (id === undefined ? method + " c" : method + " doc") {
        case "GET c":
          return find(docs, url.searchParams);
        case "POST c":
          seq += 1;
          id = "demo" + ("000000000" + seq).slice(-10);
          doc = stamped(id, payload.doc, null);
          break;
        case "GET doc":
          return prev ? Promise.resolve(copy(prev)) : refuse(404, "document not found");
        case "PUT doc":
          if (why) return refuse(409, why);
          doc = stamped(id, payload.doc, prev);
          break;
        case "PATCH doc":
          if (!prev) return refuse(404, "document not found");
          if (why) return refuse(409, why);
          var body = copy(bodyOf(prev));
          if (payload.merge !== undefined) body = mergePatch(body, bodyOf(payload.merge || {}));
          var inc = payload.$inc || {};
          for (var k in inc) {
            if (!Object.prototype.hasOwnProperty.call(inc, k)) continue;
            var cur = fieldOf(body, k);
            if (typeof inc[k] !== "number" || (cur !== undefined && typeof cur !== "number")) return refuse(400, "$inc adds numbers to numbers");
            var at = body;
            var keys = k.split(".");
            for (var j = 0; j < keys.length - 1; j++) at = at[keys[j]] = at[keys[j]] !== null && typeof at[keys[j]] === "object" ? at[keys[j]] : {};
            at[keys[keys.length - 1]] = (cur || 0) + inc[k];
          }
          doc = stamped(id, body, prev);
          break;
        case "DELETE doc":
          if (!prev) return refuse(404, "document not found");
          if (why) return refuse(409, why);
          delete docs[id];
          changed(name, id, 0);
          return Promise.resolve(null);
        default:
          return refuse(405, "method not allowed");
      }
      docs[id] = doc;
      data[name] = docs;
      changed(name, id, doc._version);
      return Promise.resolve(copy(doc));
    }

    function subscribe(fn) {
      listeners.push(fn);
      return function unsubscribe() {
        listeners = listeners.filter(function (l) { return l !== fn; });
      };
    }

    return { call: call, subscribe: subscribe };
  }

  var memory = demo ? memoryBackend() : null;

  var app = {
    collection: function (name) { return new Collection(name); },
    me: function () { return call("GET", "me"); },
    subscribe: subscribe,
  };

  global.bnblocApp = app;
  if (!Object.prototype.hasOwnProperty.call(global, "app")) global.app = app;
})(typeof window !== "undefined" ? window : globalThis, "live");
