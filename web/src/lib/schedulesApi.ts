import { req } from "./api.ts";
import type { RoomSchedule, ScheduleInput } from "./schedules.ts";
import type { ScheduleHookSecret, ScheduleRunReply } from "./wire.gen.ts";
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
  // An AI task answers what the run did instead.
  runNow: (roomId: string, id: string) =>
    req<{ sequence?: number } & Partial<ScheduleRunReply>>(`${base(roomId)}/${encodeURIComponent(id)}/run`, { method: "POST" }),
  // An AI task's dry run: what a run would do now, nothing sent.
  tryAI: (roomId: string, id: string) =>
    req<ScheduleRunReply>(`${base(roomId)}/${encodeURIComponent(id)}/run?dry=1`, { method: "POST" }),
  // A webhook task's URL and secret (the secret is answered this once):
  // make or rotate (creator), revoke (creator, owner or admin).
  rotateHook: (roomId: string, id: string) =>
    req<ScheduleHookSecret>(`${base(roomId)}/${encodeURIComponent(id)}/hook`, { method: "POST" }),
  revokeHook: (roomId: string, id: string) =>
    req<void>(`${base(roomId)}/${encodeURIComponent(id)}/hook`, { method: "DELETE", as: "none" }),
};
