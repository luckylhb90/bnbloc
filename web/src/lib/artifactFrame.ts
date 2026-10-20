// Host side of the live-artifact frame protocol (pure: no React, no DOM
// globals, so it is unit-tested under node). ArtifactRunner wires it to a
// sandboxed iframe; public/artifact-runner.{html,js} is the guest side.
//
// Why a runner page instead of srcdoc: a srcdoc frame inherits the APP's
// Content-Security-Policy, so the desktop shell had to allow inline scripts
// app-wide just so agent-made HTML could run — which let any XSS in the app
// reach Tauri IPC. The runner is loaded by URL instead, from its own origin
// with its own policy, and the app keeps `script-src 'self'`:
//   - desktop: the shell's `bnbloc-artifact` URI scheme (apps/desktop
//     src-tauri/src/artifact_scheme.rs) serves the runner with a header CSP
//     that permits inline script; the app CSP's frame-src allows only it.
//   - browser: the gateway serves the same files from the web bundle.
// Either way the iframe is sandbox="allow-scripts" WITHOUT allow-same-origin,
// so the guest has an opaque origin: no cookies/storage, no DOM access to the
// app, no same-origin API calls, and (desktop) no Tauri internals — those are
// injected into the main frame only.
//
// Message flow (every hop is postMessage; the host checks origin "null" — a
// sandboxed frame's only possible origin — and that the sender is THIS frame):
//   runner -> host : { type: "artifact-runner-ready" }       once per mount
//   host   -> runner: { type: "artifact-load", html }         once per mount
//   (runner document.write()s html into itself: from here on the frame IS the
//    artifact, and the collaboration protocol below talks to it directly)
//   guest  -> host : { type: "artifact-ready" }               on load
//   host   -> guest: { type: "artifact-hello", actor }
//   guest  -> host : { type: "artifact-state", state }        actor intent
//   host   -> guest: { type: "artifact-state", state, actor, broadcast }
//   guest  -> host : { type: "artifact-open-file", name }      a room file
// The last is a click on a relative link (the bootstrap turns it into the
// message): the host opens the room's file of that name in its own tab,
// where the member may download it. The frame itself never downloads or
// navigates (no allow-downloads, no allow-top-navigation): a page an agent
// wrote cannot put a file on the member's computer.

import { schemeUrl, type ConvertFileSrc } from "./schemeUrl.ts";
import { MaxArtifactStateBytes } from "./wire.gen.ts";

/** Desktop URI scheme serving the runner (registered in the Tauri shell). */
export const ARTIFACT_SCHEME = "bnbloc-artifact";
/** The runner page, at the web bundle root and at the scheme root. */
export const ARTIFACT_RUNNER_PAGE = "artifact-runner.html";
/** The artifact's own policy, applied inside the written document. Everything
 *  the artifact needs must already be in its content: ALL external loads are
 *  refused (no CDN pulls, no beacon exfiltration); data:/blob: stay open only
 *  for self-embedded assets. Sandbox (no allow-forms/popups/top-navigation)
 *  covers form posts and navigation of the app. */
export const ARTIFACT_CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; media-src data: blob:";

export type { ConvertFileSrc };

/** Where the iframe loads the runner from. In the desktop shell Tauri's
 *  convertFileSrc builds the platform form of the scheme URL
 *  (`bnbloc-artifact://localhost/…` on macOS/Linux,
 *  `http://bnbloc-artifact.localhost/…` on Windows); in a browser it is the
 *  runner next to the app's index.html. The fragment tells the runner which
 *  parent origin may hand it a document. */
export function artifactRunnerUrl(env: {
  convertFileSrc?: ConvertFileSrc;
  baseURI: string;
  hostOrigin: string;
}): string {
  const base = env.convertFileSrc
    ? schemeUrl(env.convertFileSrc, ARTIFACT_SCHEME, ARTIFACT_RUNNER_PAGE)
    : new URL(ARTIFACT_RUNNER_PAGE, env.baseURI).href;
  return `${base}#host=${encodeURIComponent(env.hostOrigin)}`;
}

/** The document the runner writes into the frame: the artifact policy, then
 *  the trusted readiness bootstrap, then the artifact content. The bootstrap
 *  announces readiness on window load — after every inline script has run and
 *  registered its message listener — so the host's join snapshot cannot
 *  arrive before the guest can receive it. */
export function composeArtifactDocument(src: string): string {
  return `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="referrer" content="no-referrer">
<meta http-equiv="Content-Security-Policy" content="${ARTIFACT_CSP}">
</head>
<body>
<script>
window.addEventListener("load", () => parent.postMessage({ type: "artifact-ready" }, "*"));
document.addEventListener("click", (e) => {
  const a = e.target instanceof Element ? e.target.closest("a[href]") : null;
  const href = a ? a.getAttribute("href") || "" : "";
  if (!href || href.startsWith("#") || href.startsWith("//") || /^[a-z][a-z0-9+.-]*:/i.test(href)) return;
  e.preventDefault();
  let name = href.split(/[?#]/)[0].split("/").pop() || "";
  try { name = decodeURIComponent(name); } catch {}
  if (name) parent.postMessage({ type: "artifact-open-file", name }, "*");
});
</script>
${src}
</body>
</html>`;
}

export type GuestMessage =
  | { kind: "runner-ready" }
  | { kind: "ready" }
  | { kind: "state"; state: unknown; key: string }
  | { kind: "open-file"; name: string };

/** The longest file name an artifact may ask the host to open. */
export const MAX_OPEN_FILE_NAME = 255;

/** A room file name a guest names: a base name (no path, no control
 *  characters, not "." or ".."), within MAX_OPEN_FILE_NAME; null otherwise. */
export function openFileName(name: unknown): string | null {
  if (typeof name !== "string") return null;
  const n = name.trim();
  if (!n || n === "." || n === ".." || n.length > MAX_OPEN_FILE_NAME || /[\\/\u0000-\u001f\u007f]/.test(n)) return null;
  return n;
}

/** The room file a guest's link names: the most recently updated file of
 *  that name (case-insensitive), or null. Names repeat across channels and
 *  runs; the newest is the one a page just written beside it means. */
export function pickFileByName<T extends { fileName?: string; title: string; updatedAtMs: number }>(files: readonly T[], name: string): T | null {
  const want = name.toLowerCase();
  let best: T | null = null;
  for (const f of files) {
    if ((f.fileName || f.title).toLowerCase() !== want) continue;
    if (!best || f.updatedAtMs > best.updatedAtMs) best = f;
  }
  return best;
}

/** Validates one inbound message event against the frame it must come from.
 *  Returns null for anything that is not a well-formed message from that
 *  sandboxed frame (wrong source, non-opaque origin, unknown type, oversized
 *  or non-JSON state). */
export function readGuestMessage(
  ev: { origin: string; source: unknown; data: unknown },
  frameWindow: unknown,
): GuestMessage | null {
  if (ev.origin !== "null") return null;
  if (!frameWindow || ev.source !== frameWindow) return null;
  const data = ev.data as { type?: unknown; state?: unknown; name?: unknown } | null;
  if (!data || typeof data !== "object") return null;
  switch (data.type) {
    case "artifact-runner-ready":
      return { kind: "runner-ready" };
    case "artifact-ready":
      return { kind: "ready" };
    case "artifact-state": {
      let key: string | undefined;
      try {
        key = JSON.stringify(data.state);
      } catch {
        return null;
      }
      // The room service refuses a state over MaxArtifactStateBytes (bytes).
      if (typeof key !== "string" || new TextEncoder().encode(key).length > MaxArtifactStateBytes) return null;
      return { kind: "state", state: data.state, key };
    }
    case "artifact-open-file": {
      const name = openFileName(data.name);
      return name ? { kind: "open-file", name } : null;
    }
    default:
      return null;
  }
}

/** The runner hand-off message. */
export function loadMessage(html: string): { type: "artifact-load"; html: string } {
  return { type: "artifact-load", html };
}

/** Stable short fingerprint of the artifact content, so a content change
 *  (same version) remounts the frame — the old srcdoc prop did the same. */
export function contentKey(src: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < src.length; i++) {
    h ^= src.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return `${src.length.toString(36)}-${(h >>> 0).toString(36)}`;
}
