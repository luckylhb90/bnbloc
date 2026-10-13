import { createContext, useContext } from "react";

// Whether the signed-in member owns the open room's deliverables: they may
// post in the room (lib/roomRoles mayChat over the room's member list, the
// one role source), the rule the gateway applies to publishing a
// deliverable as a work (publish-work). RoomView provides it; a
// deliverable's card offers "Publish to gallery" only then. Outside a room
// (no provider) nobody owns anything.
export const DeliverableOwner = createContext(false);

/** useDeliverableOwner reads whether the member owns the room's
 *  deliverables. */
export function useDeliverableOwner(): boolean {
  return useContext(DeliverableOwner);
}
