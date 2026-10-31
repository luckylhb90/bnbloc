// The member's activity stream (GET /v1/my/activity?rooms=, SSE): the rooms'
// unread state, then what changes, pushed instead of polled
// (docs/prd/2026-10-30-replicas-without-owners.md, part B). It reconnects
// on its own, as the room streams do (sse.ts), and every connection starts
// with the state again.

import { getToken, refreshSession } from "./session";
import { apiUrl } from "./config";
import { UnreadDeltaKind, UnreadStateKind, type UnreadDeltaPayload, type UnreadReply } from "./wire.gen.ts";

export interface ActivityHandlers {
  readonly onState: (state: UnreadReply) => void;
  readonly onDelta: (delta: UnreadDeltaPayload) => void;
  readonly onStatus?: (up: boolean) => void;
}

const BASE_DELAY_MS = 1000;
const MAX_DELAY_MS = 8_000;
const REAUTH_AFTER = 3;

/** Opens the stream for roomIds (at most wire's MaxUnreadRooms); returns
 * the function that closes it. */
export function subscribeActivity(roomIds: readonly string[], h: ActivityHandlers): () => void {
  let es: EventSource | null = null;
  let closed = false;
  let attempts = 0;
  let timer: number | null = null;

  const payloadOf = (raw: string): unknown => {
    try {
      return (JSON.parse(raw) as { payload?: unknown }).payload;
    } catch {
      return undefined;
    }
  };

  const connect = async () => {
    if (closed) return;
    if (attempts >= REAUTH_AFTER) {
      if (await refreshSession().catch(() => false)) attempts = 0;
      if (closed) return;
    }
    const token = getToken();
    const src = new EventSource(
      apiUrl(`/v1/my/activity?rooms=${encodeURIComponent(roomIds.join(","))}${token ? `&access_token=${encodeURIComponent(token)}` : ""}`),
    );
    es = src;
    src.onopen = () => {
      attempts = 0;
      h.onStatus?.(true);
    };
    src.onerror = () => {
      h.onStatus?.(false);
      if (es !== src) return;
      src.close();
      es = null;
      attempts += 1;
      if (closed || timer !== null) return;
      const delay = Math.min(MAX_DELAY_MS, BASE_DELAY_MS * 2 ** Math.min(attempts, 5));
      timer = window.setTimeout(() => {
        timer = null;
        void connect();
      }, delay * (0.5 + Math.random() * 0.5));
    };
    src.addEventListener(UnreadStateKind, (e) => {
      const p = payloadOf((e as MessageEvent).data);
      if (p && typeof p === "object") h.onState(p as UnreadReply);
    });
    src.addEventListener(UnreadDeltaKind, (e) => {
      const p = payloadOf((e as MessageEvent).data);
      if (p && typeof p === "object") h.onDelta(p as UnreadDeltaPayload);
    });
  };

  void connect();
  return () => {
    closed = true;
    if (timer !== null) clearTimeout(timer);
    es?.close();
    es = null;
  };
}
