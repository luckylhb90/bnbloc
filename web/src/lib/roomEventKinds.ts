// The room stream's event kinds. The realtime service emits SSE named events
// whose name is the event kind, and EventSource delivers a named event only
// to a listener for that name (a bare onmessage misses them all — the
// original bug), so sse.ts registers one listener per kind listed here.
//
// The kinds come from the Go registry (RoomEventKindValues, generated into
// wire.gen.ts). UNREGISTERED_ROOM_EVENT_KINDS holds only the kinds the
// registry does not name yet: a kind joins the registry by leaving this list
// (roomEventKinds.test.ts fails on an overlap).
import { RoomEventKindValues } from "./wire.gen.ts";

export const UNREGISTERED_ROOM_EVENT_KINDS: readonly string[] = [
  "agent_session_state",
  "agent_roster",
  "presence_join",
  "presence_leave",
  "presence_snapshot",
  "presence_typing",
  "message_deleted",
  "message_edited",
  // Durable agent-job lifecycle (queue status rows under the chat that spawned
  // them) and room deliverable changes (the Files panel's live refresh).
  "agent_job_created",
  "agent_job_updated",
  "artifact_updated",
  // Live collaboration state from a running artifact preview (transient).
  "artifact_state",
];

export const ROOM_EVENT_KINDS: readonly string[] = [...RoomEventKindValues, ...UNREGISTERED_ROOM_EVENT_KINDS];
