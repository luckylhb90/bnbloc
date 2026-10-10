import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { refreshSession } from "@/lib/session";
import { subscribeRoom, type RoomEvent, type RoomStreamScope } from "@/lib/sse";
import { dedupeMembers, sameUser } from "@/lib/format";
import { useScopedState } from "@/lib/useScopedState";
import { maxSequence } from "@/views/room/feedModel";
import { unreadBaseline } from "@/lib/unreadBaseline";
import { roomAccessEnded, type RoomAccessEnd } from "@/lib/roomGone";

// Case-insensitive self match, mirrored from the feed's own isSelf: the agent
// (author "agent") never equals the subject, and display-name vs userId casing
// ("Maya" vs "maya") must not flip a self check.
function isSelfUser(who: string | undefined, subject: string | null): boolean {
  if (!who || !subject) return false;
  if (who === "agent") return false;
  return who.toLowerCase() === subject.toLowerCase();
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
  // Which events the subscription receives (sse.RoomStreamScope): the room
  // view takes every channel including the people chat (its badge), the
  // people-chat panel only its own channel.
  readonly scope?: RoomStreamScope;
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
  const { roomId, subject, joinedGen, channelId, feed, historyLoaded } = params;
  const includePeople = params.scope?.includePeople ?? false;
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
    if (!channelId || baseline === null || !attentive || latest <= 0) return;
    const t = window.setTimeout(() => {
      void api.markRead(roomId, latest, channelId).catch(() => {});
    }, 400);
    return () => window.clearTimeout(t);
  }, [roomId, channelId, baseline, attentive, latest]);

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

  useEffect(() => {
    if (!channelId) return;
    setTypingUsers({});
    let off = () => {};
    off = subscribeRoom(
      roomId,
      (e: RoomEvent) => {
        // Revocation ends this stream for good: stop BEFORE the server closes
        // it, so the reconnect loop never starts.
        const ended = roomAccessEnded(e, subject);
        if (ended) {
          off();
          setConnected(false);
          onRevokedRef.current(ended);
          return;
        }
        if (e.kind === "member_removed") return;
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
        if (e.kind === "presence_join" && e.presenceUser) {
          const user = e.presenceUser;
          setOnline((o) => dedupeMembers([...o, user]));
          return;
        }
        if (e.kind === "presence_leave" && e.presenceUser) {
          setOnline((o) => o.filter((u) => !sameUser(u, e.presenceUser)));
          return;
        }
        // The subscription is room-wide so cross-channel job/artifact events
        // arrive; a message landing in another channel never enters the feed —
        // it schedules a coalesced unread true-up instead (a refetch, not a
        // local increment: reconnect replays would double-count locally).
        if (e.kind === "chat_message" || e.kind === "agent_messages") {
          const evCh = e.channelId || "";
          if (evCh && evCh !== channelId) {
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
      refreshSession,
      channelId,
      { includePeople, channelOnly },
    );
    return () => {
      off();
      if (unreadDirtyTimer.current !== null) {
        window.clearTimeout(unreadDirtyTimer.current);
        unreadDirtyTimer.current = null;
      }
    };
  }, [roomId, subject, joinedGen, channelId, includePeople, channelOnly]);

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
