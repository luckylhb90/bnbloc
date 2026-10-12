import { ROOM_EVENT_KINDS, UNREGISTERED_ROOM_EVENT_KINDS } from "./roomEventKinds.ts";
import { RoomEventKindValues } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// Every kind the Go registry names is listened for (member_joined among
// them: the member list refreshes on it), and a registered kind is not
// listed a second time by hand.
for (const k of RoomEventKindValues) {
  assert(ROOM_EVENT_KINDS.includes(k), `the stream does not listen for ${k}`);
  assert(!UNREGISTERED_ROOM_EVENT_KINDS.includes(k), `${k} is registered in Go: remove it from UNREGISTERED_ROOM_EVENT_KINDS`);
}
assert(new Set(ROOM_EVENT_KINDS).size === ROOM_EVENT_KINDS.length, "a kind is listed twice");
assert(ROOM_EVENT_KINDS.includes("member_joined"), "member_joined has a listener");
