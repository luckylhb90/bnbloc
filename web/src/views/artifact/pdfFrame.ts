// The PDF viewer's sandboxed guest and the host-side validation of what it
// says (pure: no React, no DOM globals; unit-tested under node).
//
// The PDF never runs in the app origin: pdf.js parses and renders it inside
// the artifact runner frame (sandbox="allow-scripts", opaque origin, CSP with
// no network — lib/artifactFrame). The runner writes PDF_GUEST_HTML (the same
// one-document hand-off as live artifacts); once the guest is ready the host
// posts the pdf.js library and worker SOURCES plus the file bytes, and the
// guest evaluates them as inline module scripts. pdf.js then runs its
// "worker" on the frame's main thread (no Worker can be spawned from an
// opaque origin, and none is needed) and draws pages to canvases.
//
//   host  -> guest: { type: "pdf.load", lib, worker, bytes, background }   once
//   host  -> guest: { type: "pdf.command", id: "zoom"|"fit"|"page-prev"|"page-next", value }
//   guest -> host : { type: "pdf.status", page, pages, zoom }
//   guest -> host : { type: "pdf.error", message }

import { readGuestMessage, type GuestMessage } from "../../lib/artifactFrame.ts";

export type PdfMessage =
  | GuestMessage
  | { kind: "status"; page: number; pages: number; zoom: number }
  | { kind: "error"; message: string };

const ERROR_LIMIT = 500;

function count(v: unknown, max: number): number | null {
  return typeof v === "number" && Number.isInteger(v) && v >= 0 && v <= max ? v : null;
}

/** Validates one message from the PDF guest frame (origin "null", source =
 *  that frame); null for anything else. */
export function readPdfMessage(
  ev: { origin: string; source: unknown; data: unknown },
  frameWindow: unknown,
): PdfMessage | null {
  const base = readGuestMessage(ev, frameWindow);
  if (base) return base;
  if (ev.origin !== "null" || !frameWindow || ev.source !== frameWindow) return null;
  const data = ev.data as Record<string, unknown> | null;
  if (!data || typeof data !== "object") return null;
  if (data.type === "pdf.status") {
    const page = count(data.page, 1_000_000);
    const pages = count(data.pages, 1_000_000);
    const zoom = typeof data.zoom === "number" && Number.isFinite(data.zoom) && data.zoom > 0 && data.zoom <= 64 ? data.zoom : null;
    if (page === null || pages === null || zoom === null) return null;
    return { kind: "status", page, pages, zoom };
  }
  if (data.type === "pdf.error" && typeof data.message === "string") {
    return { kind: "error", message: data.message.slice(0, ERROR_LIMIT) };
  }
  return null;
}

/** The guest document (runs only inside the sandboxed runner frame). */
export const PDF_GUEST_HTML = `<style>
html,body{margin:0;height:100%;}
body{overflow:auto;font:13px system-ui,sans-serif;color:#bbb;}
#pages{display:flex;flex-direction:column;align-items:center;gap:12px;padding:12px 8px;}
.page{position:relative;background:#fff;box-shadow:0 1px 6px rgba(0,0,0,.45);flex:none;}
.page canvas{position:absolute;inset:0;width:100%;height:100%;}
#msg{padding:16px;}
</style>
<div id="msg">Loading PDF…</div>
<div id="pages"></div>
<script>
(function () {
  "use strict";
  var host = window.parent;
  var CSS_PER_PT = 96 / 72;
  var started = false;
  var doc = null, pages = [], base = null;
  var fitWidth = true, zoom = 1, current = 1;
  var observer = null, statusTimer = 0;
  var list = document.getElementById("pages");
  var msg = document.getElementById("msg");

  function post(m) { host.postMessage(m, "*"); }
  function fail(e) {
    msg.textContent = "Could not open this PDF.";
    post({ type: "pdf.error", message: String((e && e.message) || e || "error") });
  }
  window.addEventListener("error", function (e) { if (!doc) fail(e.error || e.message); });

  // Evaluates one module source; resolves once its last statement ran.
  function runModule(src) {
    return new Promise(function (resolve, reject) {
      var id = "__pdfStep" + Math.random().toString(36).slice(2);
      var timer = setTimeout(function () { reject(new Error("pdf.js did not load")); }, 60000);
      window[id] = function () { clearTimeout(timer); delete window[id]; resolve(); };
      var s = document.createElement("script");
      s.type = "module";
      s.textContent = src + "\\n;globalThis[" + JSON.stringify(id) + "]();";
      document.head.appendChild(s);
    });
  }

  function cssScale() {
    if (!base) return 1;
    if (fitWidth) return Math.max(0.05, (document.documentElement.clientWidth - 24) / base.width);
    return zoom * CSS_PER_PT;
  }
  function status() {
    clearTimeout(statusTimer);
    statusTimer = setTimeout(function () {
      post({ type: "pdf.status", page: current, pages: pages.length, zoom: cssScale() / CSS_PER_PT });
    }, 50);
  }
  function layout() {
    var s = cssScale();
    pages.forEach(function (p) {
      var size = p.size || base;
      p.el.style.width = Math.floor(size.width * s) + "px";
      p.el.style.height = Math.floor(size.height * s) + "px";
      p.renderedAt = 0;
      if (p.canvas) { p.canvas.remove(); p.canvas = null; }
      if (observer) { observer.unobserve(p.el); observer.observe(p.el); }
    });
    status();
  }
  function render(p) {
    var s = cssScale();
    if (p.renderedAt === s || p.busy) return;
    p.busy = true;
    doc.getPage(p.n).then(function (page) {
      var vp1 = page.getViewport({ scale: 1 });
      if (!p.size || p.size.width !== vp1.width || p.size.height !== vp1.height) {
        p.size = { width: vp1.width, height: vp1.height };
        p.el.style.width = Math.floor(vp1.width * s) + "px";
        p.el.style.height = Math.floor(vp1.height * s) + "px";
      }
      var dpr = window.devicePixelRatio || 1;
      var vp = page.getViewport({ scale: s * dpr });
      var canvas = document.createElement("canvas");
      canvas.width = Math.floor(vp.width);
      canvas.height = Math.floor(vp.height);
      return page.render({ canvasContext: canvas.getContext("2d"), viewport: vp, canvas: canvas }).promise.then(function () {
        if (p.canvas) p.canvas.remove();
        p.canvas = canvas;
        p.el.appendChild(canvas);
        p.renderedAt = s;
      });
    }).catch(fail).then(function () { p.busy = false; if (p.renderedAt !== cssScale() && p.visible) render(p); });
  }
  function onScroll() {
    var top = window.scrollY || document.documentElement.scrollTop;
    var best = 1;
    for (var i = 0; i < pages.length; i++) {
      if (pages[i].el.offsetTop - 16 <= top + 1) best = i + 1; else break;
    }
    if (best !== current) { current = best; status(); }
  }
  function goTo(n) {
    var p = pages[Math.min(pages.length, Math.max(1, n)) - 1];
    if (p) window.scrollTo({ top: p.el.offsetTop - 12 });
  }
  function command(id, value) {
    if (!doc) return;
    if (id === "zoom" && typeof value === "number" && isFinite(value) && value > 0) {
      var anchor = current; fitWidth = false; zoom = Math.min(64, Math.max(0.05, value)); layout(); goTo(anchor);
    } else if (id === "fit") {
      var at = current; fitWidth = true; layout(); goTo(at);
    } else if (id === "page-prev") goTo(current - 1);
    else if (id === "page-next") goTo(current + 1);
  }

  async function load(d) {
    try {
      if (typeof d.background === "string") document.body.style.background = d.background;
      await runModule(d.worker);
      await runModule(d.lib);
      var lib = globalThis.pdfjsLib;
      doc = await lib.getDocument({
        data: new Uint8Array(d.bytes),
        isEvalSupported: false,
        useSystemFonts: true,
        enableXfa: false,
        disableAutoFetch: true,
        disableStream: true,
        verbosity: 0,
      }).promise;
      var first = (await doc.getPage(1)).getViewport({ scale: 1 });
      base = { width: first.width, height: first.height };
      msg.remove();
      observer = new IntersectionObserver(function (entries) {
        entries.forEach(function (e) {
          var p = e.target.__page;
          p.visible = e.isIntersecting;
          if (e.isIntersecting) render(p);
        });
      }, { rootMargin: "600px 0px" });
      for (var n = 1; n <= doc.numPages; n++) {
        var el = document.createElement("div");
        el.className = "page";
        var p = { n: n, el: el, canvas: null, size: null, renderedAt: 0, busy: false, visible: false };
        el.__page = p;
        pages.push(p);
        list.appendChild(el);
      }
      layout();
      window.addEventListener("scroll", onScroll, { passive: true });
      window.addEventListener("resize", function () { if (fitWidth) layout(); });
    } catch (e) {
      fail(e);
    }
  }

  window.addEventListener("message", function (ev) {
    if (ev.source !== host) return;
    var d = ev.data;
    if (!d || typeof d !== "object") return;
    if (d.type === "pdf.load" && !started && typeof d.lib === "string" && typeof d.worker === "string" && d.bytes instanceof ArrayBuffer) {
      started = true;
      load(d);
    } else if (d.type === "pdf.command" && typeof d.id === "string") {
      command(d.id, d.value);
    }
  });
})();
</script>`;
