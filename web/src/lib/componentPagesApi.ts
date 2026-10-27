import { req } from "./api.ts";
import { desktop, openExternal } from "./desktop.ts";
import { componentPageRouteHash } from "./previewRoute.ts";
import type { CardReply, ComponentCardCall, ComponentPageList, ComponentPageOpen, ComponentPageSession } from "./wire.gen.ts";

// Service components' pages (docs/architecture/service-components.md): a
// service of the organization serves a member its own pages (its settings,
// a connection to make, a change to allow) at an address carrying a ticket.
export const componentPagesApi = {
  // The pages the member may open (an admin page only for an org admin).
  list: () => req<ComponentPageList>("/v1/my/component-pages"),
  // Opens one page (for a room the member is in, with roomId): the answer
  // is its address, which the web opens in a new tab.
  open: (component: string, page: string, query?: string, roomId?: string) =>
    req<ComponentPageSession>(`/v1/my/component-pages/${encodeURIComponent(component)}/${encodeURIComponent(page)}`, {
      method: "POST",
      body: JSON.stringify({ query: query || undefined, roomId: roomId || undefined } satisfies ComponentPageOpen),
    }),
  // A card's action in place (kind "call"): the service answers what the
  // card shows now, an address to open, and whether it is done.
  callCard: (component: string, page: string, query?: string, roomId?: string) =>
    req<CardReply>(`/v1/my/component-cards/${encodeURIComponent(component)}/${encodeURIComponent(page)}`, {
      method: "POST",
      body: JSON.stringify({ query: query || undefined, roomId: roomId || undefined } satisfies ComponentCardCall),
    }),
};

// openComponentPage opens a page apart from the app, never in its place:
// in the desktop app in the system browser (the app's webview would replace
// the app with it); in a browser in a new tab, opened inside the click (so
// no popup blocker stops it) at the app's own #/open-page route, which asks
// for the page's address and replaces itself with it
// (views/OpeningComponentPage.tsx). Not about:blank: a host that embeds the
// app (a work chat's in-app browser) opens a blank window bare, without what
// it gives the app's own pages, and a page that needs that (a sign-in's
// consent) fails there.
export async function openComponentPage(component: string, page: string, query?: string, roomId?: string): Promise<void> {
  if (desktop.available()) {
    const { url } = await componentPagesApi.open(component, page, query, roomId);
    await openExternal(url);
    return;
  }
  // No fallback on a null answer: a host that opens the tab itself (and
  // denies the browser's window) answers null too, and a second tab would
  // open.
  window.open(`${window.location.pathname}${window.location.search}${componentPageRouteHash({ component, page, query, roomId })}`, "_blank");
}
