// Wire shapes of the team features served by realtime (internal/wire/team.go):
// the prompt library, chat messages marked as tasks, and global search.

export type PromptVisibility = "private" | "tenant";

export interface Prompt {
  promptId: string;
  tenantId: string;
  author: string;
  title: string;
  body: string;
  tags: string[];
  visibility: PromptVisibility;
  sourceRoomId?: string;
  sourceSeq?: number;
  useCount: number;
  createdAtMs: number;
  updatedAtMs: number;
}

export interface PromptInput {
  title: string;
  body: string;
  tags: string[];
  visibility: PromptVisibility;
  sourceRoomId?: string;
  sourceSeq?: number;
}

export interface PromptQuery {
  q?: string;
  tag?: string;
  mine?: boolean;
}

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

export type SearchKind = "message" | "artifact" | "prompt";

export interface SearchResult {
  kind: SearchKind;
  roomId?: string;
  roomName?: string;
  channelId?: string;
  // "people" when a message hit is in the room's people chat.
  channelKind?: "people";
  seq?: number;
  artifactId?: string;
  promptId?: string;
  title?: string;
  snippet: string;
  author?: string;
  atMs: number;
}

export interface SearchResponse {
  results: SearchResult[];
  nextBefore?: number;
}
