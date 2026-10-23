import { req } from "./api.ts";
import type { ComponentPageList, ComponentPageOpen, ComponentPageSession } from "./wire.gen.ts";

// Service components' pages (docs/architecture/service-components.md): a
// service of the organization serves a member its own pages (its settings,
// a connection to make, a change to allow) at an address carrying a ticket.
export const componentPagesApi = {
  // The pages the member may open (an admin page only for an org admin).
  list: () => req<ComponentPageList>("/v1/my/component-pages"),
  // Opens one page: the answer is its address, which the web opens in a
  // new tab.
  open: (component: string, page: string, query?: string) =>
    req<ComponentPageSession>(`/v1/my/component-pages/${encodeURIComponent(component)}/${encodeURIComponent(page)}`, {
      method: "POST",
      body: JSON.stringify({ query: query || undefined } satisfies ComponentPageOpen),
    }),
};

// openComponentPage opens a page in a new tab. The tab is opened at once
// (inside the click, so no popup blocker stops it) and pointed at the page
// when its address is known.
export async function openComponentPage(component: string, page: string, query?: string): Promise<void> {
  const tab = window.open("about:blank", "_blank");
  try {
    const { url } = await componentPagesApi.open(component, page, query);
    if (tab) tab.location.href = url;
    else window.location.assign(url);
  } catch (e) {
    tab?.close();
    throw e;
  }
}
