// Wire shapes of the team features served by realtime (internal/wire/team.go):
// chat messages marked as tasks and global search.

export type TaskStatus = "open" | "done";

export interface MessageTask {
  roomId: string;
  channelId?: string;
  seq: number;
  status: TaskStatus;
  title: string;
  messageAuthor?: string;
  createdBy: string;
  createdAtMs: number;
  doneBy?: string;
  doneAtMs?: number;
}

// Payload of the transient task_updated room event.
export interface TaskUpdate {
  seq: number;
  task?: MessageTask;
  removed?: boolean;
}

export type SearchKind = "message" | "artifact";

export interface SearchResult {
  kind: SearchKind;
  roomId?: string;
  roomName?: string;
  channelId?: string;
  // "people" when a message hit is in the room's people chat.
  channelKind?: "people";
  seq?: number;
  artifactId?: string;
  title?: string;
  snippet: string;
  author?: string;
  atMs: number;
}

export interface SearchResponse {
  results: SearchResult[];
  nextBefore?: number;
}
