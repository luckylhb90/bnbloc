import { req } from "./api.ts";
import { roomPath } from "./roomId.ts";
import type { RoomGroupConnect, RoomGroupsReply } from "./wire.gen.ts";

// The group chats connected to a room (docs/prd/2026-10-15-app-identity-
// schedules-notify-ai.md §5a), served by the room service. A member asks
// for a code, posts it in the group chat where the organization's bot is,
// and the chat is connected; apps and AI tasks then notify "group:<name>".
export const roomGroupsApi = {
  list: (roomId: string) => req<RoomGroupsReply>(roomPath(roomId, "groups")),
  connect: (roomId: string, name: string) =>
    req<RoomGroupConnect>(`${roomPath(roomId, "groups")}/connect`, { method: "POST", body: JSON.stringify({ name }) }),
  remove: (roomId: string, name: string) =>
    req<void>(`${roomPath(roomId, "groups")}/${encodeURIComponent(name)}`, { method: "DELETE", as: "none" }),
};
