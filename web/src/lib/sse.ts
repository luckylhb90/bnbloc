import { req } from "./api";
import { ApiError } from "./errors.ts";
import { getToken, refreshSession } from "./session";
import { apiUrl } from "./config";
import { normalizeRoomEvent, type RoomEvent } from "./roomEvents.ts";
import { ROOM_EVENT_KINDS } from "./roomEventKinds.ts";
import { roomPath } from "./roomId.ts";
import { currentTenantPolicy } from "./tenantPolicy.ts";

// Which events a room subscription receives. The default (the room view)
// is every channel; includePeople also admits the room's people chat, which
// the server withholds from every all-channels stream that does not ask for
// it (agent streams never do). channelOnly narrows the stream to channelId
// (plus room-wide events) — the people-chat panel's own stream.
export interface RoomStreamScope {
  readonly includePeople?: boolean;
  readonly channelOnly?: boolean;
  // lanes: full events only for the open channel (channelId), a coalesced
  // channel_activity for every other (channels PRD §2.4); rescope changes
  // the open channel without reopening the stream.
  readonly lanes?: boolean;
}

// A room subscription: call it to unsubscribe; rescope moves a lanes
// stream to other open channels (a no-op for any other stream).
export interface RoomSubscription {
  (): void;
  readonly rescope: (open: readonly string[]) => void;
}

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
  // Mints a fresh session after a run of failures; defaults to the app's
  // own refresh, so no subscriber goes dark after a signing-key rotation.
  onAuthError: () => Promise<boolean> = refreshSession,
  channelId?: string,
  scope: RoomStreamScope = {},
): RoomSubscription {
  let es: EventSource | null = null;
  // A lanes stream: its id (its first event) and the channels it has open,
  // which a reconnect opens with.
  let streamId = "";
  let open: readonly string[] = channelId ? [channelId] : [];
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
      // Not an event (a proxy's error page, a truncated frame): dropped,
      // never rendered as a kindless row the feed takes for an agent reply.
      return;
    }
    if (ev.kind === "stream_opened") {
      streamId = typeof ev.payload?.streamId === "string" ? ev.payload.streamId : "";
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
    if (attempts >= REAUTH_AFTER && !reauthing) {
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
    streamId = "";
    // A lanes stream names its first open channel as channelId too: a
    // realtime replica without lanes serves that channel alone.
    const channelQ = scope.lanes
      ? `&lanes=1&open=${open.map(encodeURIComponent).join(",")}${open[0] ? `&channelId=${encodeURIComponent(open[0])}` : ""}`
      : channelId
        ? `&channelId=${encodeURIComponent(channelId)}`
        : "";
    // allChannels: the room view needs cross-channel events — agent-job rows
    // and artifact updates carry the channel they originated in, while the
    // Files panel lists room-wide deliverables. Feed kinds are filtered to the
    // active channel by the caller.
    const scopeQ = scope.channelOnly ? "" : `${scope.lanes ? "" : "&allChannels=1"}${scope.includePeople ? "&includePeople=1" : ""}`;
    // Member locations: the time zone is sent only when the organization has them
    // on (pulse_geography). Off, the zone never leaves the device.
    const tzQ = currentTenantPolicy()?.pulseGeography
      ? `&tz=${encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone)}`
      : "";
    const url = apiUrl(
      `${roomPath(roomId, "events")}?${
        token ? `access_token=${encodeURIComponent(token)}` : ""
      }${channelQ}${scopeQ}${tzQ}`,
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

  const reconnectNow = () => {
    if (closed) return;
    if (es) {
      es.close();
      es = null;
    }
    void connect();
  };

  const rescope = (next: readonly string[]) => {
    if (!scope.lanes || closed) return;
    open = next;
    // Not open yet (or reconnecting): the next connection opens with it.
    if (!streamId) return;
    rescopeStream(roomId, streamId, next).then((found) => {
      // The stream is gone on the server (closed, or moved replicas):
      // reopen with the open channels.
      if (!found) reconnectNow();
    }, reconnectNow);
  };

  const off = () => {
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
  return Object.assign(off, { rescope });
}

// rescopeStream asks realtime to move a lanes stream to other open channels
// (POST .../events/scope); false when the stream is gone.
async function rescopeStream(roomId: string, streamId: string, open: readonly string[]): Promise<boolean> {
  try {
    await req<void>(roomPath(roomId, "events", "scope"), { method: "POST", body: JSON.stringify({ streamId, open }), as: "none" });
    return true;
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return false;
    throw e;
  }
}
