// isPreviewRoute recognizes a torn-out preview window: its hash route
// #/preview/<roomId>/<artifactId> is the whole window's identity (the shell
// creates the window with that hash). Kept apart from the preview view so the
// app shell can check it without loading the preview's code.
export function isPreviewRoute(): { roomId: string; artifactId: string } | null {
  const m = /^#\/preview\/([^/]+)\/([^/]+)$/.exec(window.location.hash);
  if (!m) return null;
  return { roomId: decodeURIComponent(m[1]), artifactId: decodeURIComponent(m[2]) };
}

// peopleChatRoomFromHash recognizes the popped-out people chat window:
// #/people-chat/<roomId> renders only that room's people chat. Null for any
// other hash (or an undecodable id).
export function peopleChatRoomFromHash(hash: string): string | null {
  const m = /^#\/people-chat\/([^/?]+)$/.exec(hash);
  if (!m) return null;
  try {
    return decodeURIComponent(m[1]);
  } catch {
    return null;
  }
}

// liveRouteFromHash recognizes a job's live view in its own window (a
// desktop pop-out, or a browser tab on the web): #/live/<roomId>/<jobId>.
export function liveRouteFromHash(hash: string): { roomId: string; jobId: string } | null {
  const m = /^#\/live\/([^/?]+)\/([^/?]+)$/.exec(hash);
  if (!m) return null;
  try {
    return { roomId: decodeURIComponent(m[1]), jobId: decodeURIComponent(m[2]) };
  } catch {
    return null;
  }
}

// liveRouteHash is that window's hash.
export function liveRouteHash(roomId: string, jobId: string): string {
  return `#/live/${encodeURIComponent(roomId)}/${encodeURIComponent(jobId)}`;
}

// A component page opened in a tab of its own (openComponentPage):
// #/open-page/<component>/<page>?query=..&room=.. asks for the page's
// address and replaces itself with it. The tab is opened at that address at
// once (not about:blank first): an embedding host can then tell it is the
// app's own page and open it as the app (with what the host gives the app's
// pages), where a blank window would be a bare one.
export type ComponentPageRoute = { component: string; page: string; query?: string; roomId?: string };

export function componentPageRouteHash(r: ComponentPageRoute): string {
  const q = new URLSearchParams();
  if (r.query) q.set("query", r.query);
  if (r.roomId) q.set("room", r.roomId);
  const qs = q.toString();
  return `#/open-page/${encodeURIComponent(r.component)}/${encodeURIComponent(r.page)}${qs ? `?${qs}` : ""}`;
}

export function componentPageRouteFromHash(hash: string): ComponentPageRoute | null {
  const m = /^#\/open-page\/([^/?]+)\/([^/?]*)(?:\?(.*))?$/.exec(hash);
  if (!m) return null;
  try {
    const q = new URLSearchParams(m[3] ?? "");
    return {
      component: decodeURIComponent(m[1]),
      page: decodeURIComponent(m[2]),
      ...(q.get("query") ? { query: q.get("query")! } : {}),
      ...(q.get("room") ? { roomId: q.get("room")! } : {}),
    };
  } catch {
    return null;
  }
}

// isAuxWindow: this page is a torn-out window (a preview, a people chat, a
// live page or a component page being opened) rather than an app window. It
// shows the account of the window that opened it, which the desktop shell
// binds when it creates the window.
export function isAuxWindow(): boolean {
  const hash = window.location.hash;
  return (
    isPreviewRoute() !== null ||
    peopleChatRoomFromHash(hash) !== null ||
    liveRouteFromHash(hash) !== null ||
    componentPageRouteFromHash(hash) !== null
  );
}
