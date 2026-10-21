import { req } from "./api.ts";
import type { ShareItem, ShareSession, ShareSessionRequest, ShareTargetList } from "./wire.gen.ts";

// Share targets (docs/architecture/share-targets.md): an installed component
// takes a shared item, as a card, to a conversation of another app.
export const shareTargetsApi = {
  // The targets the member's organization has.
  list: () => req<ShareTargetList>("/v1/my/share-targets"),
  // Starts sharing one item through a target: the answer is the share page's
  // address, which the web navigates to (the other app's client runs it).
  start: (name: string, item: ShareItem) =>
    req<ShareSession>(`/v1/my/share-targets/${encodeURIComponent(name)}/sessions`, {
      method: "POST",
      body: JSON.stringify({ item } satisfies ShareSessionRequest),
    }),
};
