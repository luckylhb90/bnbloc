import { getToken } from "./session";
import { apiUrl } from "./config";
import { normalizeRoomEvent, type RoomEvent } from "./roomEvents.ts";

// Which events a room subscription receives. The default (the room view)
// is every channel; includePeople also admits the room's people chat, which
// the server withholds from every all-channels stream that does not ask for
// it (agent streams never do). channelOnly narrows the stream to channelId
// (plus room-wide events) — the people-chat panel's own stream.
export interface RoomStreamScope {
  readonly includePeople?: boolean;
  readonly channelOnly?: boolean;
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
  // A room's experts, room expert and room packs changed (transient).
  "content_uses_changed",
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
