// Room templates (N3): wire shapes of the room service's /v1/room-templates
// (internal/wire/schedules.go) and pure helpers for the template UI.
import type { ScheduleRecipient, ScheduleRecurrence } from "./schedules.ts";
import { MaxStarterPromptBytes, MaxStarterPrompts } from "./wire.gen.ts";

export type TemplateVisibility = "private" | "tenant";

// A suggested agent is a HINT: after creating a room from the template the
// UI offers to invite its owner and to request their borrow authorization.
// Nothing is ever granted automatically.
export interface TemplateAgent {
  ownerId: string;
  provider: string;
  core?: string;
}

export interface TemplateSchedule {
  channel?: string; // channel NAME in the template; empty = default channel
  text: string;
  recipient: ScheduleRecipient;
  recurrence: ScheduleRecurrence;
  timezone: string;
}

export interface RoomTemplateBody {
  roomDescription?: string;
  channels: string[];
  starterPrompts: string[];
  suggestedAgents: TemplateAgent[];
  defaultMembers: string[];
  schedules: TemplateSchedule[];
}

export interface RoomTemplate {
  templateId: string;
  tenantId: string;
  name: string;
  description?: string;
  visibility: TemplateVisibility;
  createdBy: string;
  body: RoomTemplateBody;
  useCount: number;
  createdAtMs: number;
  updatedAtMs: number;
}

export interface RoomTemplateInput {
  name: string;
  description?: string;
  visibility: TemplateVisibility;
  body: RoomTemplateBody;
}

// Save-a-room-as-template request: the server captures description,
// channels, members (includeMembers) and schedules (includeSchedules); the
// member picks starter prompts and suggested agents.
export interface TemplateFromRoomInput {
  name?: string;
  description?: string;
  visibility: TemplateVisibility;
  starterPrompts: string[];
  suggestedAgents: TemplateAgent[];
  includeMembers: boolean;
  includeSchedules: boolean;
}

export interface InstantiateInput {
  roomId: string;
  name?: string;
  visibility?: "private" | "shared";
}

export interface RoomTemplateInstance {
  roomId: string;
  name: string;
  templateId: string;
  channels: string[];
  addedMembers: string[];
  failedMembers?: string[];
  schedules: string[];
  starterPrompts: string[];
  suggestedAgents: TemplateAgent[];
}

export function emptyTemplateBody(): RoomTemplateBody {
  return { roomDescription: "", channels: [], starterPrompts: [], suggestedAgents: [], defaultMembers: [], schedules: [] };
}

// templateSummary is a one-line description of what a template sets up.
export function templateSummary(b: RoomTemplateBody): string {
  const parts: string[] = [];
  const n = (count: number, one: string) => `${count} ${one}${count === 1 ? "" : "s"}`;
  if (b.channels.length) parts.push(n(b.channels.length, "channel"));
  if (b.starterPrompts.length) parts.push(n(b.starterPrompts.length, "starter prompt"));
  if (b.suggestedAgents.length) parts.push(n(b.suggestedAgents.length, "suggested agent"));
  if (b.defaultMembers.length) parts.push(n(b.defaultMembers.length, "member"));
  if (b.schedules.length) parts.push(n(b.schedules.length, "schedule"));
  return parts.length ? parts.join(" · ") : "Empty room";
}

// agentHandle renders a suggested agent as its room handle ("jack/codex").
export function agentHandle(a: TemplateAgent): string {
  return `${a.ownerId}/${a.provider}`;
}

// agentFollowUps splits a new room's suggested agents for the post-create
// offers: owners to invite (not yet members, not me) and agents whose
// borrow authorization the creator must request from their owner (any agent
// I do not own). My own agents need neither.
export function agentFollowUps(
  agents: TemplateAgent[],
  me: string,
  members: string[],
): { invite: string[]; requestBorrow: TemplateAgent[] } {
  const memberSet = new Set(members);
  const invite: string[] = [];
  const requestBorrow: TemplateAgent[] = [];
  for (const a of agents) {
    if (a.ownerId === me) continue;
    if (!memberSet.has(a.ownerId) && !invite.includes(a.ownerId)) invite.push(a.ownerId);
    requestBorrow.push(a);
  }
  return { invite, requestBorrow };
}

// Starter prompts of a freshly created room, kept for the room's first
// visits (the instantiate response is the only place they are returned).
const starterKey = (roomId: string) => `bnbloc.templateStart.${roomId}`;

export function rememberTemplateStart(inst: RoomTemplateInstance, storage: Pick<Storage, "setItem"> = localStorage): void {
  try {
    storage.setItem(starterKey(inst.roomId), JSON.stringify(inst));
  } catch {
    // Best-effort: storage full or unavailable just skips the follow-ups.
  }
}

export function takeTemplateStart(roomId: string, storage: Pick<Storage, "getItem"> = localStorage): RoomTemplateInstance | undefined {
  try {
    const raw = storage.getItem(starterKey(roomId));
    return raw ? (JSON.parse(raw) as RoomTemplateInstance) : undefined;
  } catch {
    return undefined;
  }
}

export function clearTemplateStart(roomId: string, storage: Pick<Storage, "removeItem"> = localStorage): void {
  try {
    storage.removeItem(starterKey(roomId));
  } catch {
    // ignore
  }
}

// parseLines splits an editor textarea into trimmed, non-empty, unique lines.
export function parseLines(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line && !out.includes(line)) out.push(line);
  }
  return out;
}

// formatAgentLine renders a suggested agent for the editor: "owner/provider",
// or "core/owner/provider" when it names a core (the composer's handle form).
export function formatAgentLine(a: TemplateAgent): string {
  return a.core ? `${a.core}/${a.ownerId}/${a.provider}` : `${a.ownerId}/${a.provider}`;
}

// parseAgentLines parses editor lines back into suggested agents. Lines that
// are not "owner/provider" or "core/owner/provider" are returned as invalid.
export function parseAgentLines(text: string): { agents: TemplateAgent[]; invalid: string[] } {
  const agents: TemplateAgent[] = [];
  const invalid: string[] = [];
  for (const line of parseLines(text)) {
    const parts = line.split("/").map((p) => p.trim());
    if (parts.some((p) => !p) || parts.length < 2 || parts.length > 3) {
      invalid.push(line);
      continue;
    }
    agents.push(parts.length === 3 ? { core: parts[0], ownerId: parts[1], provider: parts[2] } : { ownerId: parts[0], provider: parts[1] });
  }
  return { agents, invalid };
}

// starterCandidates picks starter-prompt candidates from a room feed: the
// newest human chat texts (no agent output, no imported history), unique,
// non-empty, within the starter-prompt size cap.
export function starterCandidates(
  feed: readonly { text: string; agentLabel?: string; importedFrom?: string }[],
  max = MaxStarterPrompts,
): string[] {
  const out: string[] = [];
  const enc = new TextEncoder();
  for (let i = feed.length - 1; i >= 0 && out.length < max; i--) {
    const m = feed[i];
    if (m.agentLabel || m.importedFrom) continue;
    const text = m.text.trim();
    if (!text || out.includes(text) || enc.encode(text).length > MaxStarterPromptBytes) continue;
    out.push(text);
  }
  return out;
}
