import { req } from "./api.ts";
import { desktop, openExternal } from "./desktop.ts";
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
// the app with it); in a browser in a new tab, opened at once (inside the
// click, so no popup blocker stops it) and pointed at the page when its
// address is known.
export async function openComponentPage(component: string, page: string, query?: string, roomId?: string): Promise<void> {
  if (desktop.available()) {
    const { url } = await componentPagesApi.open(component, page, query, roomId);
    await openExternal(url);
    return;
  }
  const tab = window.open("about:blank", "_blank");
  try {
    const { url } = await componentPagesApi.open(component, page, query, roomId);
    if (tab) tab.location.href = url;
    else await openExternal(url);
  } catch (e) {
    tab?.close();
    throw e;
  }
}
