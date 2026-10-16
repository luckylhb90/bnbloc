import { req } from "./api.ts";
import { roomPath } from "./roomId.ts";
import type {
  NoteCreateRequest,
  NoteRevisionsReply,
  NoteRollbackRequest,
  NotesReply,
  NoteUpdateRequest,
  RoomNote,
} from "./wire.gen.ts";

const base = (roomId: string) => roomPath(roomId, "notes");
const one = (roomId: string, id: string) => roomPath(roomId, "notes", id);

// A room's notes (docs/prd/2026-10-16-room-memory-and-evolution.md), served
// by the room service. Every member reads; a member who may act writes (a
// visitor is refused with 403).
export const notesApi = {
  // Every note of the room, whatever its status: the panel groups them.
  list: (roomId: string) => req<NotesReply>(base(roomId)),
  create: (roomId: string, body: NoteCreateRequest) =>
    req<RoomNote>(base(roomId), { method: "POST", body: JSON.stringify(body) }),
  // 409: the note changed since expectedRevision (or it is superseded).
  update: (roomId: string, id: string, patch: NoteUpdateRequest) =>
    req<RoomNote>(one(roomId, id), { method: "PATCH", body: JSON.stringify(patch) }),
  // Removes the note and its history.
  remove: (roomId: string, id: string) => req<void>(one(roomId, id), { method: "DELETE", as: "none" }),
  revisions: (roomId: string, id: string) => req<NoteRevisionsReply>(`${one(roomId, id)}/revisions`),
  // The note takes the content of an earlier revision, as a new revision.
  rollback: (roomId: string, id: string, revision: number) => {
    const body: NoteRollbackRequest = { revision };
    return req<RoomNote>(`${one(roomId, id)}/rollback`, { method: "POST", body: JSON.stringify(body) });
  },
};
