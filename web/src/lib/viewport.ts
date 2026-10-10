// Mobile keyboard handling (R14). The shell is sized by --app-height
// (styles.css, Shell), falling back to 100dvh. Chrome on Android shrinks the
// layout viewport for the on-screen keyboard (interactive-widget=
// resizes-content in index.html), so 100dvh already ends above it. iOS Safari
// overlays the keyboard instead and only the visual viewport shrinks; there
// the composer at the bottom of the shell would sit under the keyboard. On a
// touch device this keeps --app-height equal to the visual viewport, so the
// shell — and the composer pinned to its bottom — always fits what is visible.
// Fine pointers (desktop browsers, the desktop shell) are left untouched.

export function trackVisualViewport(): void {
  if (typeof window === "undefined" || !window.visualViewport) return;
  if (typeof window.matchMedia !== "function" || !window.matchMedia("(pointer: coarse)").matches) return;
  const vv = window.visualViewport;
  const root = document.documentElement;
  const apply = () => {
    // Pinch zoom also shrinks the visual viewport; the app must not shrink
    // with it, so a zoomed viewport keeps the last unzoomed height.
    if (Math.abs(vv.scale - 1) > 0.01) return;
    root.style.setProperty("--app-height", `${Math.round(vv.height)}px`);
    // iOS scrolls the page to reveal a focused field; the shell now fits the
    // visible area, so return the page to the top (the shell scrolls inside).
    if (window.scrollY !== 0) window.scrollTo(0, 0);
  };
  vv.addEventListener("resize", apply);
  apply();
}
