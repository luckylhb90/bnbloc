// Which empty state a message feed shows when it has nothing to list. The
// copy of each is written for one reader: the people chat's, a person
// reading a shared room they have not joined (a preview, docs/prd/2026-10-22-
// shared-room-preview.md: nothing there is "quiet", they just have not said
// anything), a room that already has an agent to talk to, and a member's
// room with none.
export type FeedEmpty = "people" | "preview" | "ready" | "quiet";

export function feedEmptyKind(room: { readonly people: boolean; readonly preview: boolean; readonly agentReady: boolean }): FeedEmpty {
  if (room.people) return "people";
  if (room.preview) return "preview";
  return room.agentReady ? "ready" : "quiet";
}
