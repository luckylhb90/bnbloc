// The room stream's event kinds. The realtime service emits SSE named events
// whose name is the event kind, and EventSource delivers a named event only
// to a listener for that name (a bare onmessage misses them all — the
// original bug), so sse.ts registers one listener per kind listed here.
//
// The kinds are the Go registry, wire.RoomEventKinds: RoomEventKindValues is
// generated from its rows (`go generate ./internal/wire`), so a kind
// registered in Go is a kind the stream listens for, with no second list.
import { RoomEventKindValues, type RoomEventKind } from "./wire.gen.ts";

export const ROOM_EVENT_KINDS: readonly RoomEventKind[] = RoomEventKindValues;
