import { req } from "./api";
import { ApiError } from "./errors.ts";
import { getToken, refreshSession } from "./session";
import { apiUrl } from "./config";
import { normalizeRoomEvent, type RoomEvent } from "./roomEvents.ts";
import { ROOM_EVENT_KINDS } from "./roomEventKinds.ts";
import { roomPath } from "./roomId.ts";
import { currentTenantPolicy } from "./tenantPolicy.ts";
import {
  MaxStreamOpen,
  MaxStreamRooms,
  RoomEventKinds,
  UnreadDeltaKind,
  UnreadStateKind,
  type StreamOpenedPayload,
  type StreamScopeRequest,
  type UnreadDeltaPayload,
  type UnreadReply,
} from "./wire.gen.ts";

// The event streams (SSE). A web tab holds ONE: the member stream (GET
// /v1/my/events, docs/prd/2026-10-30-replicas-without-owners.md §9), which
// carries the unread of the tab's rooms and the room the tab shows. The
// room view, a thread panel and the people-chat panel watch their channels
// on it (watchRoom), the shell its unread (watchUnread); what they watch is
// told to the server as the stream's scope (POST /v1/my/events/scope),
// never by reopening it. subscribeRoom is a room's own stream, for the
// pages that are one room and nothing else (the standalone preview and
// live views).
//
// EventSource cannot set headers, so the session token is passed as an
// access_token query param (the gateway accepts it on these two streams
// only). EventSource on its own dies on a 401/403 or keeps retrying with a
// stale token, so both are wrapped in a bounded exponential-backoff
// reconnect loop that mints a fresh session after a run of failures.

const BASE_DELAY_MS = 1000;
// A replica that stops ends only its own streams: one that failed a few
// times must not then wait half a minute (the reopen resumes cheaply).
const MAX_DELAY_MS = 8_000;
// After this many consecutive failures the token is refreshed before the
// next attempt.
const REAUTH_AFTER = 3;

function backoff(attempts: number): number {
  const delay = Math.min(MAX_DELAY_MS, BASE_DELAY_MS * 2 ** Math.min(attempts, 5));
  return delay * (0.5 + Math.random() * 0.5);
}

function tzQuery(): string {
  // Member locations: the time zone is sent only when the organization has
  // them on (pulse_geography). Off, the zone never leaves the device.
  return currentTenantPolicy()?.pulseGeography ? `&tz=${encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone)}` : "";
}

function parse(raw: string): RoomEvent | null {
  if (!raw) return null;
  try {
    return JSON.parse(raw) as RoomEvent;
  } catch {
    // Not an event (a proxy's error page, a truncated frame): dropped,
    // never rendered as a kindless row the feed takes for an agent reply.
    return null;
  }
}

const crossChannel = new Set(RoomEventKinds.filter((k) => k.crossChannel).map((k) => k.name as string));

// ---------------------------------------------------------------- the member stream

/** One channel of a room a surface shows (the room view's channel, an
 * open thread, the people chat). channelOnly: only that channel's events
 * (and room-wide and cross-channel ones); else every event of the room. */
export interface RoomWatch {
  /** Moves the watch to another channel of its room (no reopen). */
  readonly setChannel: (channelId: string) => void;
  /** Ends the watch. */
  readonly off: () => void;
}

export interface UnreadHandlers {
  readonly onState: (state: UnreadReply) => void;
  readonly onDelta: (delta: UnreadDeltaPayload) => void;
  readonly onStatus?: (up: boolean) => void;
}

interface Watcher {
  roomId: string;
  channelId: string;
  channelOnly: boolean;
  onEvent: (e: RoomEvent) => void;
  onStatus?: (up: boolean) => void;
}

// The open-room key of a stream showing none.
const NO_ROOM = "\u0000";

class MemberStream {
  private es: EventSource | null = null;
  private streamId = "";
  private attempts = 0;
  private timer: number | null = null;
  private reauthing = false;
  private up = false;
  private readonly watchers: Watcher[] = [];
  private readonly unread = new Set<UnreadHandlers>();
  private rooms: readonly string[] = [];
  // What the server was last told: the rooms (by key) and the open room.
  private sentRooms: string | null = null;
  private sentOpen: string | null = null;
  // The open room and the last sequence received of it (a reconnect asks
  // for what is newer).
  private openRoom = "";
  private lastSeq = 0;
  private scopeQueued = false;

  watchRoom(roomId: string, channelId: string, channelOnly: boolean, onEvent: (e: RoomEvent) => void, onStatus?: (up: boolean) => void): RoomWatch {
    const w: Watcher = { roomId, channelId, channelOnly, onEvent, onStatus };
    this.watchers.push(w);
    this.ensure();
    if (this.up) onStatus?.(true);
    this.scope();
    return {
      setChannel: (ch) => {
        if (w.channelId === ch) return;
        w.channelId = ch;
        this.scope();
      },
      off: () => {
        const i = this.watchers.indexOf(w);
        if (i >= 0) this.watchers.splice(i, 1);
        this.scope();
        this.closeIfIdle();
      },
    };
  }

  watchUnread(rooms: readonly string[], h: UnreadHandlers): () => void {
    this.rooms = rooms.slice(0, MaxStreamRooms);
    this.unread.add(h);
    this.ensure();
    if (this.up) h.onStatus?.(true);
    this.sentRooms = null; // a new watcher needs the state
    this.scope();
    return () => {
      this.unread.delete(h);
      if (this.unread.size === 0) this.rooms = [];
      this.scope();
      this.closeIfIdle();
    };
  }

  // The room the stream shows: the latest watched one, with every watched
  // channel of it open (at most MaxStreamOpen).
  private desiredOpen(): { roomId: string; channels: string[] } {
    const last = this.watchers[this.watchers.length - 1];
    if (!last) return { roomId: "", channels: [] };
    const channels: string[] = [];
    for (const w of this.watchers) {
      if (w.roomId === last.roomId && w.channelId && !channels.includes(w.channelId)) channels.push(w.channelId);
    }
    return { roomId: last.roomId, channels: channels.slice(0, MaxStreamOpen) };
  }

  private scope() {
    if (this.scopeQueued) return;
    this.scopeQueued = true;
    queueMicrotask(() => {
      this.scopeQueued = false;
      void this.sendScope();
    });
  }

  private async sendScope() {
    if (!this.streamId) return; // the stream's opening sends it
    const body: StreamScopeRequest = { streamId: this.streamId };
    const roomsKey = this.rooms.join(",");
    if (roomsKey !== this.sentRooms) body.rooms = [...this.rooms];
    const open = this.desiredOpen();
    const openKey = `${open.roomId}\u0000${open.channels.join(",")}`;
    if (openKey !== this.sentOpen) {
      if (open.roomId !== this.openRoom) {
        this.openRoom = open.roomId;
        this.lastSeq = 0;
      }
      body.open = { roomId: open.roomId, channels: open.channels, since: this.lastSeq };
    }
    if (!body.rooms && !body.open) return;
    const streamId = this.streamId;
    this.sentRooms = roomsKey;
    this.sentOpen = openKey;
    try {
      await req<void>("/v1/my/events/scope", { method: "POST", body: JSON.stringify(body), as: "none" });
    } catch (e) {
      if (streamId !== this.streamId) return;
      if (e instanceof ApiError && e.status === 403) {
        // The open room is not readable (any more): its watchers learn it
        // from their own reads; the stream goes on.
        for (const w of this.watchers) if (w.roomId === open.roomId) w.onStatus?.(false);
        return;
      }
      // Gone (another replica restarted, the stream closed): reopen.
      this.reconnectNow();
    }
  }

  private ensure() {
    if (!this.es && this.timer === null) void this.connect();
  }

  private closeIfIdle() {
    if (this.watchers.length > 0 || this.unread.size > 0) return;
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    this.es?.close();
    this.es = null;
    this.streamId = "";
    this.setUp(false);
  }

  private setUp(up: boolean) {
    if (this.up === up) return;
    this.up = up;
    for (const w of this.watchers) w.onStatus?.(up);
    for (const h of this.unread) h.onStatus?.(up);
  }

  private reconnectNow() {
    this.es?.close();
    this.es = null;
    this.streamId = "";
    void this.connect();
  }

  private scheduleReconnect() {
    if (this.timer !== null || (this.watchers.length === 0 && this.unread.size === 0)) return;
    this.timer = window.setTimeout(() => {
      this.timer = null;
      void this.connect();
    }, backoff(this.attempts));
  }

  private async connect() {
    if (this.attempts >= REAUTH_AFTER && !this.reauthing) {
      this.reauthing = true;
      try {
        if (await refreshSession().catch(() => false)) this.attempts = 0;
      } finally {
        this.reauthing = false;
      }
    }
    if (this.watchers.length === 0 && this.unread.size === 0) return;
    const token = getToken();
    const src = new EventSource(apiUrl(`/v1/my/events?${token ? `access_token=${encodeURIComponent(token)}` : ""}${tzQuery()}`));
    this.es = src;
    this.streamId = "";
    // A new stream knows nothing: everything is told again (it opens
    // showing no room).
    this.sentRooms = null;
    this.sentOpen = NO_ROOM;
    src.onerror = () => {
      this.setUp(false);
      if (this.es !== src) return;
      src.close();
      this.es = null;
      this.streamId = "";
      this.attempts += 1;
      this.scheduleReconnect();
    };
    src.addEventListener("stream_opened", (e) => {
      const p = parse((e as MessageEvent).data)?.payload as Partial<StreamOpenedPayload> | undefined;
      if (this.es !== src || !p?.streamId) return;
      this.streamId = p.streamId;
      this.attempts = 0;
      this.setUp(true);
      void this.sendScope();
    });
    src.addEventListener(UnreadStateKind, (e) => {
      const p = parse((e as MessageEvent).data)?.payload;
      if (p && typeof p === "object") for (const h of this.unread) h.onState(p as unknown as UnreadReply);
    });
    src.addEventListener(UnreadDeltaKind, (e) => {
      const p = parse((e as MessageEvent).data)?.payload;
      if (p && typeof p === "object") for (const h of this.unread) h.onDelta(p as unknown as UnreadDeltaPayload);
    });
    for (const kind of ROOM_EVENT_KINDS) {
      if (kind === "stream_opened") continue;
      src.addEventListener(kind, (e) => this.route((e as MessageEvent).data));
    }
  }

  // route hands one event of the open room to the watchers it concerns.
  private route(raw: string) {
    const ev = parse(raw);
    if (!ev || !ev.roomId || ev.roomId !== this.openRoom) return;
    if (typeof ev.sequence === "number" && ev.sequence > this.lastSeq) this.lastSeq = ev.sequence;
    for (const w of [...this.watchers]) {
      if (w.roomId !== ev.roomId) continue;
      if (w.channelOnly && ev.channelId && ev.channelId !== w.channelId && !crossChannel.has(ev.kind ?? "")) continue;
      normalizeRoomEvent(ev, w.onEvent);
    }
  }
}

let member: MemberStream | null = null;

function memberStream(): MemberStream {
  member ??= new MemberStream();
  return member;
}

/** Watches a channel of a room on the tab's member stream. */
export function watchRoom(
  roomId: string,
  channelId: string,
  channelOnly: boolean,
  onEvent: (e: RoomEvent) => void,
  onStatus?: (up: boolean) => void,
): RoomWatch {
  return memberStream().watchRoom(roomId, channelId, channelOnly, onEvent, onStatus);
}

/** Watches the unread of rooms (at most MaxStreamRooms) on the tab's
 * member stream; returns the function that stops it. */
export function watchUnread(rooms: readonly string[], h: UnreadHandlers): () => void {
  return memberStream().watchUnread(rooms, h);
}

// ---------------------------------------------------------------- a room's own stream

// subscribeRoom opens a room's own stream, every channel of it: for a page
// that is one room (the standalone preview and live views). Returns an
// unsubscribe function. onStatus(true) fires on a live connection and
// onStatus(false) while reconnecting.
export function subscribeRoom(
  roomId: string,
  onEvent: (e: RoomEvent) => void,
  onStatus?: (up: boolean) => void,
  onAuthError: () => Promise<boolean> = refreshSession,
): () => void {
  let es: EventSource | null = null;
  // The last sequence this subscription received: a reconnect asks for
  // what is newer only (since), and the stream says when it cannot
  // (stream_gap: the caller fetches the difference).
  let lastSeq = 0;
  let closed = false;
  let attempts = 0;
  let timer: number | null = null;
  let reauthing = false;

  const handle = (raw: string) => {
    const ev = parse(raw);
    if (!ev) return;
    if (typeof ev.sequence === "number" && ev.sequence > lastSeq) lastSeq = ev.sequence;
    normalizeRoomEvent(ev, onEvent);
  };

  const connect = async () => {
    if (closed) return;
    if (attempts >= REAUTH_AFTER && !reauthing) {
      reauthing = true;
      try {
        if (await onAuthError().catch(() => false)) attempts = 0;
      } finally {
        reauthing = false;
      }
      if (closed) return;
    }
    const token = getToken();
    const sinceQ = lastSeq > 0 ? `&since=${lastSeq}` : "";
    const src = new EventSource(
      apiUrl(`${roomPath(roomId, "events")}?${token ? `access_token=${encodeURIComponent(token)}` : ""}&allChannels=1${sinceQ}${tzQuery()}`),
    );
    es = src;
    src.onopen = () => {
      attempts = 0;
      onStatus?.(true);
    };
    src.onerror = () => {
      onStatus?.(false);
      if (es !== src) return;
      src.close();
      es = null;
      attempts += 1;
      if (closed || timer !== null) return;
      timer = window.setTimeout(() => {
        timer = null;
        void connect();
      }, backoff(attempts));
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
    es?.close();
    es = null;
  };
}
