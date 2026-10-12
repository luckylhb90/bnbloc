import { req } from "./api.ts";
import type { RoomSchedule, ScheduleInput } from "./schedules.ts";
import { roomPath } from "./roomId.ts";

const base = (roomId: string) => roomPath(roomId, "schedules");

// Scheduled prompts (N2), served by realtime. The creator edits/runs; the
// room owner or an admin may pause or delete any schedule.
export const schedulesApi = {
  list: (roomId: string) => req<{ schedules: RoomSchedule[] }>(base(roomId)),
  create: (roomId: string, body: ScheduleInput) =>
    req<RoomSchedule>(base(roomId), { method: "POST", body: JSON.stringify(body) }),
  update: (roomId: string, id: string, patch: Partial<ScheduleInput>) =>
    req<RoomSchedule>(`${base(roomId)}/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(patch) }),
  remove: (roomId: string, id: string) =>
    req<void>(`${base(roomId)}/${encodeURIComponent(id)}`, { method: "DELETE", as: "none" }),
  // Posts the message now, as the creator; the planned next run is unchanged.
  runNow: (roomId: string, id: string) =>
    req<{ sequence: number }>(`${base(roomId)}/${encodeURIComponent(id)}/run`, { method: "POST" }),
};
