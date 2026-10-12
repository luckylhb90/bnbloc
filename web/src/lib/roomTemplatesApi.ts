import { req } from "./api.ts";
import type {
  InstantiateInput,
  RoomTemplate,
  RoomTemplateInput,
  RoomTemplateInstance,
  TemplateFromRoomInput,
} from "./roomTemplates.ts";

const enc = encodeURIComponent;

// Room templates (N3), served by the room service. Edit/delete are
// author-only; org admins remove any team-visible template through the
// manage_org-gated /v1/tenant path.
export const roomTemplatesApi = {
  list: (mine = false) => req<{ templates: RoomTemplate[] }>(`/v1/room-templates${mine ? "?mine=1" : ""}`),
  get: (id: string) => req<RoomTemplate>(`/v1/room-templates/${enc(id)}`),
  create: (body: RoomTemplateInput) =>
    req<RoomTemplate>("/v1/room-templates", { method: "POST", body: JSON.stringify(body) }),
  update: (id: string, patch: Partial<RoomTemplateInput>) =>
    req<RoomTemplate>(`/v1/room-templates/${enc(id)}`, { method: "PATCH", body: JSON.stringify(patch) }),
  remove: (id: string) => req<void>(`/v1/room-templates/${enc(id)}`, { method: "DELETE", as: "none" }),
  moderate: (id: string) => req<void>(`/v1/tenant/room-templates/${enc(id)}`, { method: "DELETE", as: "none" }),
  // Room owner/admin: save a room as a template.
  fromRoom: (roomId: string, body: TemplateFromRoomInput) =>
    req<RoomTemplate>(`/v1/room-templates/from-room/${enc(roomId)}`, { method: "POST", body: JSON.stringify(body) }),
  // Creates a room (the caller owns it) and applies the template: channels,
  // default members, DISABLED schedules. Suggested agents come back as hints.
  instantiate: (id: string, body: InstantiateInput) =>
    req<RoomTemplateInstance>(`/v1/room-templates/${enc(id)}/instantiate`, { method: "POST", body: JSON.stringify(body) }),
};
