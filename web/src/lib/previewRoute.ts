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

// isAuxWindow: this page is a torn-out window (a preview, a people chat or a
// live page) rather than an app window. It shows the account of the window
// that opened it, which the desktop shell binds when it creates the window.
export function isAuxWindow(): boolean {
  const hash = window.location.hash;
  return isPreviewRoute() !== null || peopleChatRoomFromHash(hash) !== null || liveRouteFromHash(hash) !== null;
}
