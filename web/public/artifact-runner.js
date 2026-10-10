// Live-artifact runner: receives the artifact document from the host via
// postMessage and writes it into this frame. Guest side of the protocol in
// web/src/lib/artifactFrame.ts — keep the message shapes in sync.
//
// Refuses to run unless it is
//   - framed (it has a parent to talk to), and
//   - sandboxed WITHOUT allow-same-origin (self.origin is the opaque "null").
// The second check matters in the browser, where the runner is served from
// the app's own origin: opened top-level or framed without the sandbox it
// would render arbitrary HTML with the app's origin (storage, session token).
// Sandboxed, whatever it renders has no privilege at all.
//
// It accepts exactly one document, only from its parent window, only from
// the parent origin named in the URL fragment (#host=<origin>).
(function () {
  "use strict";

  var host = "";
  try {
    host = new URLSearchParams(location.hash.slice(1)).get("host") || "";
  } catch (e) {
    host = "";
  }

  if (window.parent === window || self.origin !== "null" || !host) {
    document.addEventListener("DOMContentLoaded", function () {
      document.body.textContent = "This page only runs inside a sandboxed artifact preview.";
    });
    return;
  }

  var loaded = false;
  function onMessage(ev) {
    if (loaded || ev.source !== window.parent || ev.origin !== host) return;
    var data = ev.data;
    if (!data || typeof data !== "object" || data.type !== "artifact-load") return;
    if (typeof data.html !== "string") return;
    loaded = true;
    window.removeEventListener("message", onMessage);
    // document.open() keeps this Window (the host's frame.contentWindow
    // reference and its source check stay valid) and the document's CSP;
    // the written document adds the artifact's own policy on top.
    document.open();
    document.write(data.html);
    document.close();
  }

  window.addEventListener("message", onMessage);
  window.parent.postMessage({ type: "artifact-runner-ready" }, "*");
})();
