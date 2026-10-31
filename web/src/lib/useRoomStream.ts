import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { watchRoom, type RoomWatch } from "@/lib/sse";
import type { RoomEvent } from "@/lib/roomEvents";
import { dedupeMembers, sameUser } from "@/lib/format";
import { useScopedState } from "@/lib/useScopedState";
import { maxSequence } from "@/views/room/feedModel";
import { unreadBaseline } from "@/lib/unreadBaseline";
import { roomAccessEnded, type RoomAccessEnd } from "@/lib/roomGone";
import { addChannelActivity, takeChannelActivity } from "@/lib/channelActivity";
import type { ChannelActivityPayload, PresenceDeltaPayload } from "@/lib/wire.gen";

// Self match, mirrored from the feed's own isSelf: the agent (author "agent")
// never equals the subject; user ids compare exactly (sameUser).
function isSelfUser(who: string | undefined, subject: string | null): boolean {
  if (who === "agent") return false;
  return sameUser(who, subject);
}

interface UseRoomStreamParams {
  readonly roomId: string;
  readonly subject: string | null;
  // Bumped after a (re)join so the stream re-subscribes with the newly-scoped
  // session token.
  readonly joinedGen: number;
  // The active channel; null until the rail selects one (nothing subscribes
  // or marks read before that).
  readonly channelId: string | null;
  // The current channel's feed rows (only their sequences are read).
  readonly feed: readonly { readonly seq?: number }[];
  // True once the channel's latest history page has loaded: the unread
  // baseline is computed against it.
  readonly historyLoaded: boolean;
  // The reader has not joined the room (a preview): no read cursor to move.
  readonly preview?: boolean;
  // Delegate for every event kind the room owns (feed, jobs, artifacts,
  // working state, roster). The stream only intercepts typing, presence, and
  // the cross-channel unread guard.
  readonly onMessageEvent: (e: RoomEvent) => void;
  // Called on a reconnect transition so the room can catch up on whatever the
  // stream missed while down.
  readonly onReconnect: () => void;
  // Access to the room ended (it was deleted, or this member was removed).
  // The stream is already stopped — it must not reconnect into a 403 loop.
  readonly onRevoked: (reason: RoomAccessEnd) => void;
  // The open channel may lack events: a lanes stream saw activity in it (a
  // message that landed while the stream was re-scoped to it), or the
  // stream said it has a gap (stream_gap). The feed catches up.
  readonly onCatchUp?: () => void;
  // Which events the watch receives on the tab's member stream: the room
  // view takes every event of the room (its channel in full, the others'
  // activity, the people chat for its badge); a thread or the people-chat
  // panel only its own channel (channelOnly).
  readonly scope?: { readonly channelOnly?: boolean };
}

interface UseRoomStream {
  connected: boolean;
  // Surfaced only after the stream has been down briefly, so an initial connect
  // or a momentary blip does not flash "Reconnecting…".
  showReconnecting: boolean;
  online: string[];
  typingUsers: Record<string, number>;
  unreadByChannel: Record<string, number>;
  // Sequence the member had already read when this channel was opened, so the
  // feed can draw the "New" divider above the first unread message.
  unreadFromSeq: number | null;
  // Reconnect catch-up counter, a dependency for loads that must true-up.
  liveReload: number;
  // Re-read the per-channel unread (another surface — the people-chat panel —
  // moved a read cursor this stream did not see).
  refreshUnread: () => void;
}

// GAP_SETTLE_MS is when a stream gap's difference is read the second time.
const GAP_SETTLE_MS = 3000;

// The member is looking at the room only while the document is visible AND
// focused. Reading state must not advance otherwise (a background tab or an
// unfocused desktop window), or unread counts and notifications never fire.
function isAttentive(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "visible" && document.hasFocus();
}

function useAttentive(): boolean {
  const [attentive, setAttentive] = useState(isAttentive);
  useEffect(() => {
    const update = () => setAttentive(isAttentive());
    document.addEventListener("visibilitychange", update);
    window.addEventListener("focus", update);
    window.addEventListener("blur", update);
    return () => {
      document.removeEventListener("visibilitychange", update);
      window.removeEventListener("focus", update);
      window.removeEventListener("blur", update);
    };
  }, []);
  return attentive;
}

// useRoomStream owns the room's realtime unit: the SSE subscription lifecycle,
// connection status, presence, typing, and the unread/read cursors.
export function useRoomStream(params: UseRoomStreamParams): UseRoomStream {
  const { roomId, subject, joinedGen, channelId, feed, historyLoaded, preview = false } = params;
  const channelOnly = params.scope?.channelOnly ?? false;
  const scope = channelId ?? "";

  const [connected, setConnected] = useState(false);
  const [showReconnecting, setShowReconnecting] = useState(false);
  const [online, setOnline] = useState<string[]>([]);
  const [typingUsers, setTypingUsers] = useState<Record<string, number>>({});
  // Per-channel unread for this room's rail. Event-driven: refetched on
  // channel switch, on a coalesced burst of other-channel messages, and on
  // reconnect. The shell's poll owns the room-level badge; this hook does not
  // poll on a timer (one poller per signal).
  const [unreadByChannel, setUnreadByChannel] = useState<Record<string, number>>({});
  const [unreadDirty, setUnreadDirty] = useState(0);
  const unreadDirtyTimer = useRef<number | null>(null);
  // Whether an unread fetch STARTED in this channel scope has landed: only
  // such a count reflects this channel before any read cursor moved.
  const [unreadFresh, setUnreadFresh] = useScopedState(scope, () => false);
  // The "New" divider baseline for this channel visit. Captured once, from
  // the fresh count and the loaded history; until then nothing is marked read
  // (marking read first would zero the count the baseline is computed from).
  const [baseline, setBaseline] = useScopedState<{ readonly seq: number | null } | null>(scope, () => null);
  const [liveReload, setLiveReload] = useState(0);
  const attentive = useAttentive();
  const refreshUnread = useCallback(() => setUnreadDirty((n) => n + 1), []);

  const onMessageEventRef = useRef(params.onMessageEvent);
  onMessageEventRef.current = params.onMessageEvent;
  const onReconnectRef = useRef(params.onReconnect);
  onReconnectRef.current = params.onReconnect;
  const onRevokedRef = useRef(params.onRevoked);
  onRevokedRef.current = params.onRevoked;
  const onCatchUpRef = useRef(params.onCatchUp);
  onCatchUpRef.current = params.onCatchUp;
  // The active channel as the stream's handler reads it: the watch is not
  // reopened when it changes.
  const channelRef = useRef(channelId);
  channelRef.current = channelId;
  const watchRef = useRef<RoomWatch | null>(null);
  // The last activity sequence counted per channel (lib/channelActivity).
  const activitySeen = useRef<Record<string, number>>({});

  const wasConnected = useRef(false);
  useEffect(() => {
    if (connected) {
      setShowReconnecting(false);
      if (wasConnected.current) {
        setLiveReload((n) => n + 1);
        onReconnectRef.current();
      }
      wasConnected.current = true;
      return;
    }
    wasConnected.current = false;
    const t = window.setTimeout(() => setShowReconnecting(true), 1500);
    return () => window.clearTimeout(t);
  }, [connected]);

  useEffect(() => {
    if (!channelId) return;
    let cancelled = false;
    api
      .roomsUnread([roomId])
      .then((res) => {
        if (cancelled) return;
        const next: Record<string, number> = {};
        for (const r of res.channels?.[roomId] ?? []) next[r.channelId] = r.count;
        setUnreadByChannel(next);
        setUnreadFresh(true);
      })
      .catch(() => {
        if (cancelled) return;
        setUnreadByChannel({});
        setUnreadFresh(true);
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, channelId, unreadDirty, liveReload, setUnreadFresh]);

  useEffect(() => {
    if (!channelId || baseline !== null || !unreadFresh || !historyLoaded) return;
    setBaseline({ seq: unreadBaseline(feed, unreadByChannel[channelId] ?? 0) });
  }, [channelId, baseline, unreadFresh, historyLoaded, feed, unreadByChannel, setBaseline]);

  // Mark the channel read as its latest sequence advances — only while the
  // member is actually looking, and only after the visit's baseline exists.
  // Becoming attentive again (tab shown, window focused) flushes it.
  const latest = maxSequence(feed);
  useEffect(() => {
    if (preview || !channelId || baseline === null || !attentive || latest <= 0) return;
    const t = window.setTimeout(() => {
      void api.markRead(roomId, latest, channelId).catch(() => {});
    }, 400);
    return () => window.clearTimeout(t);
  }, [roomId, channelId, baseline, attentive, latest, preview]);

  // Prune expired typing entries so the hint disappears when they stop.
  useEffect(() => {
    if (Object.keys(typingUsers).length === 0) return;
    const id = window.setInterval(() => {
      setTypingUsers((cur) => {
        const now = Date.now();
        const next: Record<string, number> = {};
        let changed = false;
        for (const [u, exp] of Object.entries(cur)) {
          if (exp > now) next[u] = exp;
          else changed = true;
        }
        return changed ? next : cur;
      });
    }, 1000);
    return () => window.clearInterval(id);
  }, [typingUsers]);

  // The watch follows the active channel (the member stream is re-scoped,
  // not reopened); it opens once the rail has chosen one.
  const hasChannel = channelId !== null;
  useEffect(() => {
    setTypingUsers({});
    if (channelId) watchRef.current?.setChannel(channelId);
  }, [channelId]);

  useEffect(() => {
    const opening = channelRef.current;
    if (!hasChannel || !opening) return;
    let watch: RoomWatch | null = null;
    watch = watchRoom(
      roomId,
      opening,
      channelOnly,
      (e: RoomEvent) => {
        // Revocation ends this watch for good (the member stream goes on).
        const ended = roomAccessEnded(e, subject);
        if (ended) {
          watch?.off();
          setConnected(false);
          onRevokedRef.current(ended);
          return;
        }
        if (e.kind === "presence_typing" && e.presenceUser) {
          if (!isSelfUser(e.presenceUser, subject)) {
            const user = e.presenceUser;
            setTypingUsers((cur) => ({ ...cur, [user]: Date.now() + 4000 }));
          }
          return;
        }
        if (e.kind === "presence_snapshot" && e.presenceMembers) {
          setOnline(dedupeMembers(e.presenceMembers));
          return;
        }
        // Who came and who went since the room's last delta (one a second).
        if (e.kind === "presence_delta") {
          const d = e.payload as Partial<PresenceDeltaPayload> | undefined;
          const joined = d?.joined ?? [];
          const left = d?.left ?? [];
          setOnline((o) => dedupeMembers([...o.filter((u) => !left.some((l) => sameUser(u, l))), ...joined]));
          return;
        }
        // The subscription is room-wide so cross-channel job/artifact events
        // arrive; a message landing in another channel never enters the feed —
        // it schedules a coalesced unread true-up instead (a refetch, not a
        // local increment: reconnect replays would double-count locally). A
        // lanes stream sends another channel's messages as its activity.
        const active = channelRef.current;
        // The stream lacks events (it reopened past its room's buffer, or
        // fell behind): fetch the difference as after a reconnect, without
        // reopening (Telegram's updatesTooLong).
        if (e.kind === "stream_gap") {
          onCatchUpRef.current?.();
          setLiveReload((n) => n + 1);
          onReconnectRef.current();
          // A room that just changed owner may still take a message on the
          // one leaving for a moment: read the difference once more after
          // the owners agree (they do within about two seconds).
          window.setTimeout(() => onCatchUpRef.current?.(), GAP_SETTLE_MS);
          return;
        }
        if (e.kind === "channel_activity") {
          if (e.channelId && e.channelId === active) onCatchUpRef.current?.();
          else {
            const a = e.payload as Partial<ChannelActivityPayload> | undefined;
            // Taken once here, added in a pure updater (React may call an
            // updater twice).
            const taken = takeChannelActivity(activitySeen.current, a);
            if (taken) setUnreadByChannel((cur) => addChannelActivity(cur, taken));
          }
          return;
        }
        if (e.kind === "chat_message" || e.kind === "agent_messages") {
          const evCh = e.channelId || "";
          if (evCh && evCh !== active) {
            if (unreadDirtyTimer.current === null) {
              unreadDirtyTimer.current = window.setTimeout(() => {
                unreadDirtyTimer.current = null;
                setUnreadDirty((n) => n + 1);
              }, 300);
            }
            return;
          }
        }
        onMessageEventRef.current(e);
      },
      setConnected,
    );
    watchRef.current = watch;
    return () => {
      watch?.off();
      watchRef.current = null;
      if (unreadDirtyTimer.current !== null) {
        window.clearTimeout(unreadDirtyTimer.current);
        unreadDirtyTimer.current = null;
      }
    };
  }, [roomId, subject, joinedGen, hasChannel, channelOnly]);

  return {
    connected,
    showReconnecting,
    online,
    typingUsers,
    unreadByChannel,
    unreadFromSeq: baseline?.seq ?? null,
    liveReload,
    refreshUnread,
  };
}
