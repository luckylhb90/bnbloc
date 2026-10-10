import type { ChatAttachment, TurnMetrics } from "./api";
import type { TaskUpdate } from "./teamModels";
import { getToken } from "./session";
import { apiUrl } from "./config";
import { importedEditedAtMs, importedFrom } from "./handoff.ts";
import { scheduledMarker } from "./schedules.ts";

export interface RoomEvent {
  kind?: string;
  sequence?: number;
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
  // For agent_session_state / agent_messages: which agent this concerns, as an
  // "<owner>/<provider>" key + a display label, so a room with several agents
  // shows a per-agent working indicator instead of one ambiguous bubble.
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
  // For task_updated: a message task was marked, toggled or unmarked.
  taskUpdate?: TaskUpdate;
  // For chat_message: whether it was addressed to an agent (payload
  // recipient with a provider). False is a people-only message — agents
  // still read it as room context, they just do not reply.
  toAgent?: boolean;
}

// Which events a room subscription receives. The default (the room view)
// is every channel; includePeople also admits the room's people chat, which
// the server withholds from every all-channels stream that does not ask for
// it (agent streams never do). channelOnly narrows the stream to channelId
// (plus room-wide events) — the people-chat panel's own stream.
export interface RoomStreamScope {
  readonly includePeople?: boolean;
  readonly channelOnly?: boolean;
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

function isTurnMetrics(value: unknown): value is TurnMetrics {
  if (!value || typeof value !== "object") return false;
  return (
    "inputTokens" in value &&
    typeof value.inputTokens === "number" &&
    "outputTokens" in value &&
    typeof value.outputTokens === "number" &&
    "cacheCreationTokens" in value &&
    typeof value.cacheCreationTokens === "number" &&
    "cacheReadTokens" in value &&
    typeof value.cacheReadTokens === "number" &&
    "thinkingTokens" in value &&
    typeof value.thinkingTokens === "number" &&
    "durationApiMs" in value &&
    typeof value.durationApiMs === "number" &&
    "ttftMs" in value &&
    typeof value.ttftMs === "number" &&
    "costUsd" in value &&
    typeof value.costUsd === "number" &&
    "reported" in value &&
    typeof value.reported === "boolean" &&
    (!("model" in value) || typeof value.model === "string")
  );
}

// The realtime service emits SSE named events whose name is the event Kind.
// EventSource delivers named events only to matching listeners, so we register
// one per kind (a bare onmessage misses them all — the original bug).
const ROOM_EVENT_KINDS = [
  "chat_message",
  "agent_messages",
  "agent_session_state",
  "agent_roster",
  "presence_join",
  "presence_leave",
  "presence_snapshot",
  "presence_typing",
  "message_deleted",
  "message_edited",
  // Durable agent-job lifecycle (queue status rows under the chat that spawned
  // them) and room deliverable changes (the Files panel's live refresh).
  "agent_job_created",
  "agent_job_updated",
  "artifact_updated",
  // Live collaboration state from a running artifact preview (transient).
  "artifact_state",
  // Access revocation (transient): the server closes the stream right after.
  "member_removed",
  "room_deleted",
  // Message tasks (transient): the room's task strip updates live.
  "task_updated",
];

// subscribeRoom opens an SSE stream for a room's live events. EventSource cannot
// set headers, so the session token is passed as an access_token query param
// (the gateway accepts it). Returns an unsubscribe function.
//
// EventSource on its own dies permanently on a 401/403 (or keeps retrying with a
// now-stale token) and gives up on some network drops, so the room silently goes
// dark. We wrap it in a bounded exponential-backoff reconnect loop: on error we
// close the socket, report status:down, and re-open with a fresh token after a
// growing (capped) delay. After a run of consecutive failures — the signature of
// a stale token EventSource can't recover from — we invoke onAuthError to mint a
// fresh session token, then reconnect with it. onStatus(true) fires on a live
// connection and onStatus(false) while reconnecting, so the UI can show
// live/reconnecting. The returned unsubscribe stops all of this permanently.
export function subscribeRoom(
  roomId: string,
  onEvent: (e: RoomEvent) => void,
  onStatus?: (up: boolean) => void,
  onAuthError?: () => Promise<boolean>,
  channelId?: string,
  scope: RoomStreamScope = {},
): () => void {
  let es: EventSource | null = null;
  let closed = false;
  let attempts = 0;
  let timer: number | null = null;
  let reauthing = false;
  // After this many consecutive failures we treat it as an auth problem and try
  // to refresh the token before the next attempt.
  const REAUTH_AFTER = 3;
  const BASE_DELAY_MS = 1000;
  const MAX_DELAY_MS = 30_000;

  const handle = (raw: string) => {
    if (!raw) return;
    let ev: RoomEvent;
    try {
      ev = JSON.parse(raw);
    } catch {
      onEvent({ text: raw });
      return;
    }
    normalizeRoomEvent(ev, onEvent);
  };

  const scheduleReconnect = () => {
    if (closed || timer !== null) return;
    // Exponential backoff with a cap; jitter avoids a thundering herd of tabs.
    const delay = Math.min(MAX_DELAY_MS, BASE_DELAY_MS * 2 ** Math.min(attempts, 5));
    const jittered = delay * (0.5 + Math.random() * 0.5);
    timer = window.setTimeout(() => {
      timer = null;
      void connect();
    }, jittered);
  };

  const connect = async () => {
    if (closed) return;
    // A run of failures usually means the token went stale (signing-key
    // rotation) — EventSource can't recover on its own, so refresh once before
    // reconnecting, then reset the failure counter on success.
    if (attempts >= REAUTH_AFTER && onAuthError && !reauthing) {
      reauthing = true;
      try {
        const ok = await onAuthError().catch(() => false);
        if (ok) attempts = 0;
      } finally {
        reauthing = false;
      }
      if (closed) return;
    }
    const token = getToken();
    const channelQ = channelId ? `&channelId=${encodeURIComponent(channelId)}` : "";
    // allChannels: the room view needs cross-channel events — agent-job rows
    // and artifact updates carry the channel they originated in, while the
    // Files panel lists room-wide deliverables. Feed kinds are filtered to the
    // active channel by the caller.
    const scopeQ = scope.channelOnly ? "" : `&allChannels=1${scope.includePeople ? "&includePeople=1" : ""}`;
    const url = apiUrl(
      `/v1/rooms/${encodeURIComponent(roomId)}/events?${
        token ? `access_token=${encodeURIComponent(token)}` : ""
      }${channelQ}${scopeQ}`,
    );
    const src = new EventSource(url);
    es = src;
    src.onopen = () => {
      attempts = 0;
      onStatus?.(true);
    };
    src.onerror = () => {
      // EventSource fires onerror on both transient blips and hard closes; close
      // it ourselves and drive our own backoff loop for deterministic behavior.
      onStatus?.(false);
      if (es === src) {
        src.close();
        es = null;
        attempts += 1;
        scheduleReconnect();
      }
    };
    for (const kind of ROOM_EVENT_KINDS) {
      src.addEventListener(kind, (e) => handle((e as MessageEvent).data));
    }
    // Fallback for any unnamed/default events.
    src.onmessage = (e) => handle(e.data);
  };

  void connect();

  return () => {
    closed = true;
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
    if (es) {
      es.close();
      es = null;
    }
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
    // Agent replies must use the room (hub) sequence — the same space as chat —
    // for dedup and read-tracking. The per-message `m.seq` is the local core's
    // message sequence (a different, much larger space); mixing it into the feed
    // poisons the read cursor (unread breaks). A batch shares one envelope seq,
    // so offset each message by its index to keep them distinct and ordered
    // within the room space.
    const base = ev.sequence ?? 0;
    list.forEach((m, i) => {
      const mp = (m.payload as Record<string, unknown>) || {};
      const text = (mp.text as string) || "";
      if (!text) return;
      const role = (mp.role as string) || "assistant";
      const provider = (mp.provider as string) || "";
      const owner = (mp.ownerUserId as string) || "";
      const handle = provider ? (owner ? `${owner}/${provider}` : provider) : undefined;
      const metrics = isTurnMetrics(mp.metrics) ? mp.metrics : undefined;
      const thinking = typeof mp.thinking === "string" && mp.thinking ? mp.thinking : undefined;
      emit({
        kind: "agent_messages",
        sequence: base + i / 1000, // fractional offset preserves in-batch order
        roomId: ev.roomId,
        channelId: ev.channelId,
        text,
        createdAtMs: (m.createdAtUnixMs as number) || ev.atUnixMs,
        author: role === "assistant" ? "agent" : owner || role,
        agentLabel: handle,
        metrics,
        thinking,
        // An assistant reply clears its own agent's working indicator.
        agentKey: role === "assistant" && handle ? handle.toLowerCase() : undefined,
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
    // A previously-edited message loaded from history carries editedAtMs, so the
    // feed can show an "(edited)" marker without a live message_edited event.
    if (typeof p.editedAtMs === "number") ev.editedAtMs = p.editedAtMs;
    // Imported history: mark the row and keep its original edit stamp.
    ev.importedFrom = importedFrom(p);
    ev.editedAtMs ??= importedEditedAtMs(p);
    ev.scheduledId = scheduledMarker(p)?.scheduleId;
    const recipient = p.recipient as { provider?: unknown } | undefined;
    ev.toAgent = typeof recipient?.provider === "string" && recipient.provider !== "";
  } else if (ev.kind === "agent_session_state") {
    const state = (p.state as Record<string, unknown>) || {};
    const phase = state.phase as string | undefined;
    ev.agentPhase = phase === "working" ? "working" : "idle";
    const owner = (p.ownerUserId as string) || "";
    const provider = (p.provider as string) || "";
    // Key + label the agent this state is for, so multiple agents in one room
    // each get their own indicator. Fall back to "agent" when unlabeled.
    if (owner && provider) {
      ev.agentLabel = `${owner}/${provider}`;
      ev.agentKey = ev.agentLabel.toLowerCase();
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
