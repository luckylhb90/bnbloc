// Wire shapes of the team features served by realtime (internal/wire/team.go):
// chat messages marked as tasks and global search.

export type { MessageTask, SearchResponse, SearchResult } from "./wire.gen.ts";

export type { TaskUpdatedPayload as TaskUpdate } from "./wire.gen.ts";

export type { TaskStatus } from "./wire.gen.ts";

// A prompt hit is a prompt of a prompt pack the member can see (no room).
export type { SearchResultKind as SearchKind } from "./wire.gen.ts";

