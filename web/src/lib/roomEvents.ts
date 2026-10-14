// Room events as the feed reads them: the normalized RoomEvent shape and
// normalizeRoomEvent, which maps a raw event from the room stream or the
// history endpoint (they share the {kind, payload, sequence, atUnixMs}
// shape) into feed-ready events. Pure (no browser APIs), so it is
// unit-tested; sse.ts owns the live subscription.
import type { ChatAttachment, TurnMetrics } from "./api";
import type { TaskUpdate } from "./teamModels";
import type { FileRef, RoomEventKind } from "./wire.gen.ts";
import { importedEditedAtMs, importedFrom } from "./handoff.ts";
import { scheduledMarker } from "./schedules.ts";
import { dispatchAttribution, type DispatchAttribution } from "./dispatch.ts";
import { expertMarker, type ExpertMarker } from "./contentComponents.ts";
import { agentHandle, agentIdentity } from "./roomAgents.ts";

export interface RoomEvent {
  kind?: RoomEventKind;
  // The room (hub) sequence: always an integer, the same space as chat.
  sequence?: number;
  // For agent_messages: the message's index within its batch. A batch
  // shares one room sequence, so (sequence, part) orders and identifies a
  // fanned-out reply; absent for every other kind.
  part?: number;
  roomId?: string;
  // The channel the event is scoped to; "" (absent) is room-wide. The room
  // subscription is allChannels, so feed kinds arriving for a channel other
  // than the active one are filtered client-side.
  channelId?: string;
  payload?: Record<string, unknown>;
  atUnixMs?: number;
  // Normalized fields the room feed renders:
  text?: string;
  author?: string;
  // For agent_messages: the specific agent provider (e.g. "codex") to label.
  agentLabel?: string;
  // Millisecond timestamp of the underlying message, for display.
  createdAtMs?: number;
  // For agent_session_state: the agent's working/idle phase, so the UI can
  // show a live "agent is working…" indicator.
  agentPhase?: "working" | "idle";
  // For agent_session_state / agent_messages: which agent this concerns, as
  // its agentIdentity (owner, core, provider) + a display label, so a room
  // with several agents shows a per-agent working indicator instead of one
  // ambiguous bubble.
  agentKey?: string;
  // For presence_*: the full member list (snapshot) or the single member
  // (join/leave) the event concerns, so the UI can show who is online.
  presenceMembers?: string[];
  presenceUser?: string;
  // For message_deleted: the room sequence of the message to remove.
  deletedSeq?: number;
  // For message_edited: the room sequence of the message to patch and its new
  // text.
  editedSeq?: number;
  // Millisecond timestamp of the last edit; set for message_edited and carried
  // through chat_message history so a previously-edited message shows a marker.
  editedAtMs?: number;
  clientMsgId?: string;
  attachments?: readonly ChatAttachment[];
  metrics?: TurnMetrics;
  thinking?: string;
  // For agent_roster: the room's advertised agents (owner + core + provider),
  // so the composer picker can offer real drivable agents without the gateway
  // reaching any user's machine.
  rosterAgents?: { ownerUserId: string; core?: string; provider: string; sandboxed?: boolean }[];
  // For member_removed: the member whose access ended.
  removedUser?: string;
  // For chat_message / agent_messages written by a handoff import: the source
  // room's name (from the server-stamped payload.imported marker).
  importedFrom?: string;
  // For chat_message posted by a scheduled prompt (payload.scheduled): the
  // schedule id, so the feed can badge it.
  scheduledId?: string;
  // For chat_message an agent dispatched on its author's behalf (payload
  // dispatchedBy): the dispatching agent and the addressed one.
  dispatch?: DispatchAttribution;
  // For chat_message asking its agent to wear an expert (payload.expert).
  expert?: ExpertMarker;
  // For chat_message about a room file (payload.fileRef: the workbench's
  // Ask AI panel).
  fileRef?: FileRef;
  // For agent_messages that are a job's notice line, not a reply
  // (payload.notice: "progress", "member-timeout").
  notice?: string;
  // For a notice: the agent's own error output it carries (payload.detail),
  // shown fenced under the notice line.
  noticeDetail?: string;
  // For agent_messages a job wrote (replies and notices): the job and the
  // room sequence of its request (payload.jobId, payload.sourceSeq), so the
  // feed nests delegated work under the request that started it.
  jobId?: string;
  sourceSeq?: number;
  // For task_updated: a message task was marked, toggled or unmarked.
  taskUpdate?: TaskUpdate;
  // For chat_message: whether it was addressed to an agent (payload
  // recipient with a provider). False is a people-only message — agents
  // still read it as room context, they just do not reply.
  toAgent?: boolean;
}

function isChatAttachment(value: unknown): value is ChatAttachment {
  if (!value || typeof value !== "object") return false;
  return (
    "id" in value &&
    typeof value.id === "string" &&
    "filename" in value &&
    typeof value.filename === "string" &&
    "contentType" in value &&
    typeof value.contentType === "string" &&
    "size" in value &&
    typeof value.size === "number"
  );
}

// readTurnMetrics reads a reply's free-form metrics map the way the server
// bills it (wire.TurnUsageFromMetrics; both are pinned to
// internal/wire/testdata/turn_usage.json): an empty map is no usage;
// reported=false means tokens and cost are unknown (zero, not measured);
// an absent or negative count is 0; a cost is present only when reported,
// numeric and not negative. The display-only keys (cache, thinking, timing)
// default to 0 the same way.
export function readTurnMetrics(value: unknown): TurnMetrics | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const m = value as Record<string, unknown>;
  if (Object.keys(m).length === 0) return undefined;
  const model = typeof m.model === "string" && m.model.trim() ? m.model.trim() : undefined;
  const reported = m.reported !== false;
  const count = (k: string) => {
    const v = m[k];
    return reported && typeof v === "number" && v > 0 ? Math.trunc(v) : 0;
  };
  const cost = reported && typeof m.costUsd === "number" && m.costUsd >= 0 ? m.costUsd : undefined;
  return {
    ...(model ? { model } : {}),
    reported,
    inputTokens: count("inputTokens"),
    outputTokens: count("outputTokens"),
    cacheCreationTokens: count("cacheCreationTokens"),
    cacheReadTokens: count("cacheReadTokens"),
    thinkingTokens: count("thinkingTokens"),
    durationApiMs: count("durationApiMs"),
    ttftMs: count("ttftMs"),
    ...(cost !== undefined ? { costUsd: cost } : {}),
  };
}
// normalizeRoomEvent maps a raw room event (from the SSE stream or the history
// endpoint, which share the {kind, payload, sequence, atUnixMs} shape) into the
// feed-ready RoomEvent(s), emitting one or more via `emit`. agent_messages fans
// out one event per assistant message; other kinds normalize in place.
export function normalizeRoomEvent(ev: RoomEvent, emit: (e: RoomEvent) => void): void {
  const p = ev.payload || {};
  if (ev.kind === "agent_messages") {
    const list = Array.isArray(p.messages) ? (p.messages as Record<string, unknown>[]) : [];
    // Agent replies use the room (hub) sequence — the same space as chat —
    // for dedup and read-tracking. The per-message `m.seq` is the local core's
    // message sequence (a different, much larger space); mixing it into the feed
    // poisons the read cursor (unread breaks). A batch shares one envelope seq;
    // `part` (the message's index) keeps its messages distinct and ordered.
    list.forEach((m, i) => {
      const mp = (m.payload as Record<string, unknown>) || {};
      const text = (mp.text as string) || "";
      if (!text) return;
      const role = (mp.role as string) || "assistant";
      const provider = (mp.provider as string) || "";
      const owner = (mp.ownerUserId as string) || "";
      const core = typeof mp.core === "string" ? mp.core : "";
      const handle = provider ? agentHandle({ ownerId: owner, core, provider }) : undefined;
      const metrics = readTurnMetrics(mp.metrics);
      const thinking = typeof mp.thinking === "string" && mp.thinking ? mp.thinking : undefined;
      // A notice line (a team lead's progress) is no reply: it leaves the
      // agent's working indicator alone.
      const notice = typeof mp.notice === "string" && mp.notice ? mp.notice : undefined;
      const noticeDetail = notice && typeof mp.detail === "string" && mp.detail.trim() ? mp.detail : undefined;
      const jobId = typeof mp.jobId === "string" && mp.jobId ? mp.jobId : undefined;
      const sourceSeq = typeof mp.sourceSeq === "number" && mp.sourceSeq > 0 ? mp.sourceSeq : undefined;
      emit({
        kind: "agent_messages",
        sequence: ev.sequence,
        part: i,
        roomId: ev.roomId,
        channelId: ev.channelId,
        text,
        createdAtMs: (m.createdAtUnixMs as number) || ev.atUnixMs,
        author: role === "assistant" ? "agent" : owner || role,
        agentLabel: handle,
        metrics,
        thinking,
        // An assistant reply clears its own agent's working indicator.
        agentKey:
          role === "assistant" && owner && provider && !notice ? agentIdentity({ ownerId: owner, core, provider }) : undefined,
        ...(notice ? { notice } : {}),
        ...(noticeDetail ? { noticeDetail } : {}),
        ...(jobId ? { jobId } : {}),
        ...(sourceSeq ? { sourceSeq } : {}),
        importedFrom: importedFrom(p),
      });
    });
    return;
  }
  if (ev.kind === "chat_message") {
    ev.text = (p.text as string) || "";
    ev.author = (p.userId as string) || (p.author as string);
    ev.createdAtMs = (p.atUnixMs as number) || ev.atUnixMs;
    if (typeof p.clientMsgId === "string") ev.clientMsgId = p.clientMsgId;
    if (Array.isArray(p.attachments)) {
      const attachments = p.attachments.filter(isChatAttachment);
      if (attachments.length > 0) ev.attachments = attachments;
    }
    // A history row (wire.FeedMessage) carries the message's edit stamp as its
    // own editedAtMs, the RoomEvent field of the same name, so the feed shows
    // "(edited)" without a live message_edited event. Imported history keeps
    // its original edit stamp in the import marker.
    ev.importedFrom = importedFrom(p);
    ev.editedAtMs ??= importedEditedAtMs(p);
    ev.scheduledId = scheduledMarker(p)?.scheduleId;
    ev.dispatch = dispatchAttribution(p);
    ev.expert = expertMarker(p);
    ev.fileRef = fileRefOf(p.fileRef);
    const recipient = p.recipient as { provider?: unknown } | undefined;
    ev.toAgent = typeof recipient?.provider === "string" && recipient.provider !== "";
  } else if (ev.kind === "agent_session_state") {
    const state = (p.state as Record<string, unknown>) || {};
    const phase = state.phase as string | undefined;
    ev.agentPhase = phase === "working" ? "working" : "idle";
    const owner = (p.ownerUserId as string) || "";
    const provider = (p.provider as string) || "";
    const core = typeof p.core === "string" ? p.core : "";
    // Key + label the agent this state is for (the reply that ends its turn
    // clears the same key), so multiple agents in one room each get their
    // own indicator. Fall back to "agent" when unlabeled.
    if (owner && provider) {
      ev.agentLabel = agentHandle({ ownerId: owner, core, provider });
      ev.agentKey = agentIdentity({ ownerId: owner, core, provider });
    }
    ev.text = "";
  } else if (ev.kind === "agent_roster") {
    ev.rosterAgents = Array.isArray(p.agents)
      ? (p.agents as { ownerUserId: string; core?: string; provider: string; sandboxed?: boolean }[])
      : [];
    ev.text = "";
  } else if (ev.kind && ev.kind.startsWith("presence")) {
    if (Array.isArray(p.members)) ev.presenceMembers = p.members as string[];
    if (typeof p.userId === "string") ev.presenceUser = p.userId;
  } else if (ev.kind === "member_removed") {
    if (typeof p.userId === "string") ev.removedUser = p.userId;
  } else if (ev.kind === "task_updated") {
    if (typeof p.seq === "number") ev.taskUpdate = p as unknown as TaskUpdate;
  } else if (ev.kind === "message_deleted") {
    if (typeof p.sequence === "number") ev.deletedSeq = p.sequence;
  } else if (ev.kind === "message_edited") {
    if (typeof p.sequence === "number") ev.editedSeq = p.sequence;
    ev.text = (p.text as string) || "";
    ev.editedAtMs = typeof p.editedAtMs === "number" ? p.editedAtMs : ev.atUnixMs;
  }
  emit(ev);
}

// fileRefOf reads a chat message's payload.fileRef (undefined when absent
// or malformed).
export function fileRefOf(v: unknown): FileRef | undefined {
  if (!v || typeof v !== "object") return undefined;
  const r = v as Record<string, unknown>;
  if (typeof r.artifactId !== "string" || !r.artifactId || typeof r.version !== "number" || r.version < 1) return undefined;
  const sel = r.selection as Record<string, unknown> | undefined;
  return {
    artifactId: r.artifactId,
    version: r.version,
    ...(typeof r.name === "string" && r.name ? { name: r.name } : {}),
    // The part pointed at, by its label (the detail is the agent's).
    ...(sel && typeof sel.label === "string" && sel.label ? { selection: { label: sel.label } } : {}),
  };
}
