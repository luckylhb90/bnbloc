import { req } from "./api.ts";
import type { RoomComponentDraft, RoomInstallReply, RoomInstallRequest } from "./roomComponents.ts";
import { roomPath } from "./roomId.ts";

const enc = encodeURIComponent;

// Room components (services/gateway/room_components.go): what "Save as
// component" captures of a room (its owner or an admin), and a new room
// made from one. The save itself is the content form's
// (contentApi.create / newVersion).
export const roomComponentsApi = {
  draft: (roomId: string) => req<RoomComponentDraft>(roomPath(roomId, "component-draft")),
  createRoom: (componentId: string, body: RoomInstallRequest) =>
    req<RoomInstallReply>(`/v1/content-components/${enc(componentId)}/create-room`, { method: "POST", body: JSON.stringify(body) }),
};
