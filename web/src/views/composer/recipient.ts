// The composer's recipient precedence, in one place (web M17): an in-text
// @handle naming a room agent wins, else the picked recipient, else my
// selected local target as my own agent. send() drives resolveRecipient;
// the model picker, the expert's "runs on" line and the team lead read the
// explicit part (a local target has its own catalog and label).
import { agentHandle } from "../../lib/roomAgents.ts";
import type { RoomAgent } from "./types.ts";

// mentionedAgent finds an in-text "@handle" (agentHandle: core-qualified
// when the agent has a core) naming a room agent. Tokens are compared as
// exact handles, never interpolated into a RegExp, so providers with
// regex-special characters (acp:hermes) cannot break or falsely match.
export function mentionedAgent(text: string, roomAgents: readonly RoomAgent[]): RoomAgent | undefined {
  const handles = new Set((text.match(/(?:^|\s)@([\w/:.-]+)/g) || []).map((tok) => tok.trim().slice(1)));
  return roomAgents.find((a) => handles.has(agentHandle(a)));
}

// explicitRecipient is the agent the member named: an in-text @handle,
// else the picked recipient.
export function explicitRecipient(text: string, roomAgents: readonly RoomAgent[], recipient: RoomAgent | null): RoomAgent | null {
  return mentionedAgent(text, roomAgents) ?? recipient ?? null;
}

// resolveRecipient is the agent a send drives (null: nobody to drive). My
// local target is my own agent only once I am known: a recipient always
// names its owner (the server refuses an empty ownerId; no bridge would
// claim one).
export function resolveRecipient(input: {
  readonly text: string;
  readonly roomAgents: readonly RoomAgent[];
  readonly recipient: RoomAgent | null;
  readonly localTarget: { readonly provider: string; readonly core?: string } | undefined;
  readonly subject: string | null;
}): RoomAgent | null {
  const named = explicitRecipient(input.text, input.roomAgents, input.recipient);
  if (named) return named;
  const t = input.localTarget;
  return t?.provider && input.subject ? { ownerId: input.subject, provider: t.provider, core: t.core || undefined, self: true } : null;
}
