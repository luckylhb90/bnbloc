// What a room's link opens for a person who has not joined it, from the
// room's two settings: how it is joined (visibility) and whether it may be
// read first (previewable, docs/prd/2026-10-22-shared-room-preview.md). One
// sentence, said where the link is copied and in the room's settings.

import type { RoomVisibility } from "./wire.gen.ts";

export function roomLinkAudience(visibility: RoomVisibility, previewable: boolean): string {
  const shared = visibility === "shared";
  if (previewable) {
    return shared
      ? "Anyone in your organization can open and read it, and join with a click."
      : "Anyone in your organization can open and read it. Joining needs an invite code.";
  }
  return shared
    ? "Anyone in your organization can open it and join with a click, but not read it first."
    : "Only members can open it. Others need an invite code first.";
}
