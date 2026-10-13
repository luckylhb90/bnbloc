// Pure parse/serialize of the app's hash routes (useHashRoute owns the
// browser side). Kept free of React so node tests can import it.
//
// Route shapes:
//   #/                     -> { view: "plaza" }
//   #/plaza | #/myrooms | #/agents | #/team | #/components | #/org | #/directory | #/releases
//   #/room/<roomId>        -> a specific room
//   #/room/<roomId>?seq=<n>[&channel=<channelId>]
//                          -> the room, scrolled to one message (a
//                             notification deep link): the message anchor
//                             is handed to the feed's jump mechanism
//   #/room/<roomId>?artifact=<artifactId>[&v=<n>]
//                          -> the room with one deliverable (optionally one
//                             version) opened in the preview workbench (a
//                             "copy link" from a workbench tab)
//   #/preview/<room>/<art> -> handled elsewhere (StandalonePreview)
//   #/share/<token>        -> the public share page (no sign-in); main.tsx
//                             renders it before auth (shareTokenFromHash)
//
// The SSO callback uses a key=value hash (#sessionToken=...&subject=...) which
// auth.tsx consumes and clears before the app renders; that shape never starts
// with "/" so it can never be mistaken for a route here.

export type AppRoute =
  | { kind: "view"; view: string }
  | {
      kind: "room";
      roomId: string;
      seq?: number;
      channelId?: string;
      artifactId?: string;
      artifactVersion?: number;
    };

const DEFAULT: AppRoute = { kind: "view", view: "plaza" };

export function parseHashRoute(hash: string): AppRoute {
  const h = hash.replace(/^#/, "");
  // Not a path-style hash (e.g. an SSO callback or empty): default view.
  if (!h.startsWith("/")) return DEFAULT;
  const q = h.indexOf("?");
  const path = q >= 0 ? h.slice(0, q) : h;
  const parts = path.slice(1).split("/").filter(Boolean);
  if (parts.length === 0) return DEFAULT;
  if (parts[0] === "room" && parts[1]) {
    let roomId: string;
    try {
      roomId = decodeURIComponent(parts[1]);
    } catch {
      return DEFAULT;
    }
    const route: AppRoute = { kind: "room", roomId };
    if (q >= 0) {
      const params = new URLSearchParams(h.slice(q + 1));
      const seq = Number(params.get("seq"));
      if (Number.isInteger(seq) && seq > 0) {
        route.seq = seq;
        const channel = params.get("channel");
        if (channel) route.channelId = channel;
      }
      const artifact = params.get("artifact");
      if (artifact && /^[A-Za-z0-9_-]{1,128}$/.test(artifact)) {
        route.artifactId = artifact;
        const v = Number(params.get("v"));
        if (Number.isInteger(v) && v > 0) route.artifactVersion = v;
      }
    }
    return route;
  }
  return { kind: "view", view: parts[0] };
}

// routeHash builds the canonical hash for a route so navigation and the URL
// bar stay in sync. A room's message anchor is serialized too (a deep link
// survives being copied); App drops it from the URL once it is handed off.
export function routeHash(route: AppRoute): string {
  if (route.kind === "room") {
    const base = `#/room/${encodeURIComponent(route.roomId)}`;
    const params = new URLSearchParams();
    if (route.seq) {
      params.set("seq", String(route.seq));
      if (route.channelId) params.set("channel", route.channelId);
    }
    if (route.artifactId) {
      params.set("artifact", route.artifactId);
      if (route.artifactVersion) params.set("v", String(route.artifactVersion));
    }
    const query = params.toString();
    return query ? `${base}?${query}` : base;
  }
  if (route.view === "plaza") return "#/";
  return `#/${route.view}`;
}

// artifactLinkUrl is the absolute room-member link to one deliverable
// (optionally one version): the app's base (the gateway root in the desktop
// shell, the page itself in a browser) plus the room route carrying the
// artifact. Opening it signs in like any room link and opens the workbench
// tab; the gateway still gates the room by membership.
export function artifactLinkUrl(base: string, roomId: string, artifactId: string, version?: number): string {
  return appRoot(base) + routeHash({ kind: "room", roomId, artifactId, artifactVersion: version });
}

// appRoot is the app's document URL without query or fragment. A directory
// base gains its trailing slash; a document base (".../index.html") is kept.
export function appRoot(base: string): string {
  const url = new URL(base);
  url.hash = "";
  url.search = "";
  if (!url.pathname.endsWith("/") && !/\.[^/]+$/.test(url.pathname)) url.pathname += "/";
  return url.href;
}

// A share token as the room service mints it: `bsh_<shareId>_<secret>`
// (base64url secret). The page accepts the URL-safe alphabet only, so a
// fragment can never smuggle a path into the public API calls.
const SHARE_TOKEN = /^[A-Za-z0-9_-]{8,256}$/;

/** The token of a public share page hash (`#/share/<token>`), else null. */
export function shareTokenFromHash(hash: string): string | null {
  const m = /^#\/share\/([^/?#]+)\/?$/.exec(hash);
  return m && SHARE_TOKEN.test(m[1]) ? m[1] : null;
}

/** The public link of a share: the app's base (the gateway root in the
 *  desktop shell, the page origin in a browser) plus `#/share/<token>`. */
export function shareLinkUrl(base: string, token: string): string {
  return `${appRoot(base)}#/share/${token}`;
}
