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
//   host  -> guest: { type: "pdf.command", id: "zoom"|"fit"|"page"|"page-prev"|"page-next", value }
//   guest -> host : { type: "pdf.status", page, pages, zoom }
//   guest -> host : { type: "pdf.error", message }
//   guest -> host : { type: "pdf.selection", text, page, rect }  (text "" = cleared)
//   guest -> host : { type: "pdf.thumbnail", png }  once, page 1's top as a card cover
//
// The pages carry pdf.js's text layer (transparent text over each canvas), so
// text can be selected; the guest reports the selection with where it shows
// in the frame (the viewer's box) and the host's Ask AI panel names it.
//
// Compare reads a PDF the same way, in a hidden runner frame
// (compare/pdfUnits.ts): pdf.units instead of pdf.load, answered with each
// page's text, cut to the frame protocol's unit limits inside the guest.
//
//   host  -> guest: { type: "pdf.units", lib, worker, bytes }   once
//   guest -> host : { type: "pdf.units", pages: [text], truncated }

import { readGuestMessage, type GuestMessage } from "../../lib/artifactFrame.ts";
import { UNITS_BYTES_LIMIT, UNITS_LIMIT, UNIT_TEXT_LIMIT, readUnits, type Unit } from "../../../previewer-sdk/src/protocol.ts";

export type PdfMessage =
  | GuestMessage
  | { kind: "status"; page: number; pages: number; zoom: number }
  | { kind: "error"; message: string }
  | { kind: "thumbnail"; png: ArrayBuffer }
  | { kind: "selection"; text: string; page: number; rect: { x: number; y: number; w: number; h: number } | null }
  | { kind: "units"; units: readonly Unit[]; truncated: boolean };

const ERROR_LIMIT = 500;
// The room refuses a thumbnail above this (services/room/artifact_files.go).
const THUMBNAIL_LIMIT = 512 << 10;
// A selection is read as text: bounded here, then again by the protocol.
const SELECTION_TEXT_LIMIT = 4000;
const RECT_LIMIT = 10_000_000;

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
  if (data.type === "pdf.thumbnail") {
    const png = data.png;
    return png instanceof ArrayBuffer && png.byteLength > 0 && png.byteLength <= THUMBNAIL_LIMIT ? { kind: "thumbnail", png } : null;
  }
  if (data.type === "pdf.selection" && typeof data.text === "string") {
    if (data.text === "") return { kind: "selection", text: "", page: 0, rect: null };
    const page = count(data.page, 1_000_000);
    const r = data.rect as Record<string, unknown> | null;
    const num = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) && Math.abs(v) <= RECT_LIMIT ? v : null);
    const rect = r && typeof r === "object" && num(r.x) !== null && num(r.y) !== null && num(r.w) !== null && num(r.h) !== null && (r.w as number) >= 0 && (r.h as number) >= 0
      ? { x: r.x as number, y: r.y as number, w: r.w as number, h: r.h as number }
      : null;
    if (page === null || page < 1) return null;
    return { kind: "selection", text: data.text.slice(0, SELECTION_TEXT_LIMIT), page, rect };
  }
  if (data.type === "pdf.error" && typeof data.message === "string") {
    return { kind: "error", message: data.message.slice(0, ERROR_LIMIT) };
  }
  if (data.type === "pdf.units" && Array.isArray(data.pages) && data.pages.every((t) => typeof t === "string")) {
    const units = readUnits((data.pages as string[]).map((text, i) => ({ label: `Page ${i + 1}`, text, at: `page:${i + 1}` })));
    return units ? { kind: "units", units, truncated: data.truncated === true } : null;
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
.textLayer{position:absolute;inset:0;overflow:clip;line-height:1;text-align:initial;forced-color-adjust:none;z-index:1;--user-unit:1;--total-scale-factor:calc(var(--scale-factor,1) * var(--user-unit));--scale-round-x:1px;--scale-round-y:1px;}
.textLayer :is(span,br){color:transparent;position:absolute;white-space:pre;cursor:text;transform-origin:0% 0%;}
.textLayer .markedContent{display:contents;}
.textLayer ::selection{background:rgba(0,100,255,.3);}
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
  var observer = null, statusTimer = 0, selFrame = 0, hadSel = false, laidOutAt = 0, coverSent = false;
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
    laidOutAt = Date.now();
    var s = cssScale();
    pages.forEach(function (p) {
      var size = p.size || base;
      p.el.style.width = Math.floor(size.width * s) + "px";
      p.el.style.height = Math.floor(size.height * s) + "px";
      p.renderedAt = 0;
      if (p.canvas) { p.canvas.remove(); p.canvas = null; }
      if (p.text) { p.text.remove(); p.text = null; }
      if (observer) { observer.unobserve(p.el); observer.observe(p.el); }
    });
    status();
  }
  // The card's picture: the top of page 1, at the room's cover size (16:10).
  function cover(src) {
    try {
      var w = 960, h = 600;
      var sh = Math.min(src.height, Math.round(src.width * h / w));
      var t = document.createElement("canvas");
      t.width = w; t.height = h;
      var c = t.getContext("2d");
      c.fillStyle = "#fff";
      c.fillRect(0, 0, w, h);
      c.drawImage(src, 0, 0, src.width, sh, 0, 0, w, Math.round(sh * w / src.width));
      t.toBlob(function (blob) {
        if (!blob) return;
        blob.arrayBuffer().then(function (ab) { host.postMessage({ type: "pdf.thumbnail", png: ab }, "*", [ab]); });
      }, "image/png");
    } catch (e) { /* no cover: the card keeps its icon */ }
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
        if (p.n === 1 && !coverSent) { coverSent = true; cover(canvas); }
        // Transparent text over the canvas, so text can be selected.
        if (p.text) p.text.remove();
        var layer = document.createElement("div");
        layer.className = "textLayer";
        layer.style.setProperty("--scale-factor", String(s));
        p.text = layer;
        p.el.appendChild(layer);
        return new globalThis.pdfjsLib.TextLayer({ textContentSource: page.streamTextContent(), container: layer, viewport: page.getViewport({ scale: s }) }).render().catch(function () {});
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
    } else if (id === "page" && typeof value === "number" && isFinite(value)) goTo(Math.round(value));
    else if (id === "page-prev") goTo(current - 1);
    else if (id === "page-next") goTo(current + 1);
  }

  // The member's text selection, with the page it starts in and where it
  // shows (the frame is the viewer's box); an empty text clears it.
  // force: the member's own click or Escape. A selection that vanished
  // because the pages were laid out again (the panel beside the viewer opened,
  // the zoom changed: the text layer is rebuilt) is not the member clearing it.
  function postSelection(force) {
    selFrame = 0;
    var sel = document.getSelection();
    var text = sel && !sel.isCollapsed ? sel.toString().replace(/\\s+/g, " ").trim() : "";
    if (!text) {
      if (hadSel && (force === true || Date.now() - laidOutAt > 3000)) { hadSel = false; post({ type: "pdf.selection", text: "" }); }
      return;
    }
    var node = sel.anchorNode;
    var el = node && (node.nodeType === 1 ? node : node.parentElement);
    var pageEl = el && el.closest ? el.closest(".page") : null;
    if (!pageEl || !pageEl.__page) return;
    var r = sel.getRangeAt(0).getBoundingClientRect();
    hadSel = true;
    post({ type: "pdf.selection", text: text.slice(0, 4000), page: pageEl.__page.n, rect: { x: r.left, y: r.top, w: r.width, h: r.height } });
  }
  function soon() { if (!selFrame) selFrame = requestAnimationFrame(function () { postSelection(false); }); }
  document.addEventListener("pointerup", function () { setTimeout(function () { postSelection(true); }, 0); });
  document.addEventListener("keyup", function (e) { if (e.key === "Escape" || e.shiftKey) setTimeout(function () { postSelection(true); }, 0); });
  window.addEventListener("scroll", function () { if (hadSel) soon(); }, { passive: true });
  window.addEventListener("resize", function () { if (hadSel) soon(); });

  // pdf.js, then the document: the one way the guest opens a PDF.
  async function open(d) {
    await runModule(d.worker);
    await runModule(d.lib);
    return globalThis.pdfjsLib.getDocument({
      data: new Uint8Array(d.bytes),
      isEvalSupported: false,
      useSystemFonts: true,
      enableXfa: false,
      disableAutoFetch: true,
      disableStream: true,
      verbosity: 0,
    }).promise;
  }

  // Each page's text, for Compare: cut to the unit limits here.
  async function units(d) {
    try {
      var pdf = await open(d);
      var count = Math.min(pdf.numPages, ${UNITS_LIMIT});
      var out = [], size = 0;
      for (var n = 1; n <= count; n++) {
        var content = await (await pdf.getPage(n)).getTextContent();
        var text = content.items.map(function (it) { return (it.str || "") + (it.hasEOL ? "\\n" : " "); }).join("");
        text = text.replace(/[ \\t]+\\n/g, "\\n").replace(/[ \\t]{2,}/g, " ").trim();
        text = text.length > ${UNIT_TEXT_LIMIT} ? text.slice(0, ${UNIT_TEXT_LIMIT - 1}) + "…" : text;
        // Stay well inside the host's byte limit (labels and JSON add some).
        size += text.length + 32;
        if (size > ${Math.floor(UNITS_BYTES_LIMIT * 0.9)}) { count = n - 1; break; }
        out.push(text);
      }
      post({ type: "pdf.units", pages: out, truncated: pdf.numPages > out.length });
    } catch (e) {
      fail(e);
    }
  }

  async function load(d) {
    try {
      if (typeof d.background === "string") document.body.style.background = d.background;
      doc = await open(d);
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
        var p = { n: n, el: el, canvas: null, text: null, size: null, renderedAt: 0, busy: false, visible: false };
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
    } else if (d.type === "pdf.units" && !started && typeof d.lib === "string" && typeof d.worker === "string" && d.bytes instanceof ArrayBuffer) {
      started = true;
      units(d);
    } else if (d.type === "pdf.command" && typeof d.id === "string") {
      command(d.id, d.value);
    }
  });
})();
</script>`;
