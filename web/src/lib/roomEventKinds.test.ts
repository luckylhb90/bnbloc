import { ROOM_EVENT_KINDS } from "./roomEventKinds.ts";
import { RoomEventKinds } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// The stream listens for exactly the registry's kinds, in its order:
// wire.RoomEventKinds is the one source (member_joined among them: the
// member list refreshes on it).
assert(
  JSON.stringify(ROOM_EVENT_KINDS) === JSON.stringify(RoomEventKinds.map((k) => k.name)),
  "ROOM_EVENT_KINDS is not the registry's kinds",
);
assert(new Set(ROOM_EVENT_KINDS).size === ROOM_EVENT_KINDS.length, "a kind is registered twice");
assert(ROOM_EVENT_KINDS.includes("member_joined"), "member_joined has a listener");
