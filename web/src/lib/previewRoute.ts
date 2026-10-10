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
