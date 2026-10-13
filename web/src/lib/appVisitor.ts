// A public link's app visitor token: the one owner on the web (boundary rule
// A1). An interactive app opened from a public share link is a visitor to
// its own data API: the gateway wants a visitor token (`X-Bnbloc-Visitor`)
// minted for that link, and the app's frame has an opaque origin with no
// storage of its own. So the share page holds the token
// (docs/prd/2026-10-12-interactive-apps.md §4.3):
//
//   - it mints one on first need (the frame's first request) through
//     POST /v1/public/shares/{token}/visitor and keeps it in this browser,
//     per share (visitorStorageKey), so a returning visitor stays the same
//     visitor while the token lives;
//   - it renews it before it expires (same visitor: the held token is the
//     renewal's body) and pushes the renewed grant to the frame unasked;
//   - it answers the frame's handshake request, and only a request whose
//     source is its own app frame's window (the frame's origin is opaque,
//     so the window is the only thing to check):
//
//       frame  -> parent: {type: AppVisitorRequest, v: AppHandshakeVersion}
//       parent -> frame:  {type: AppVisitorGrant, v: AppHandshakeVersion, token}
//
// A member's site ticket needs no visitor token (the ticket is the member),
// so the member's site viewer answers nothing: the SDK asks again only when
// a call is refused for a missing visitor token, which a ticket never is.
//
// Pure (no React, no DOM globals beyond the injected ones); the share page's
// app view (views/share/SharedApp.tsx) wires it to the window and its frame.

import { keyFor, readStored, StorageKey, writeStored } from "./storageKeys.ts";
import { AppHandshakeVersion, AppVisitorGrant, AppVisitorRequest, type AppVisitorToken } from "./wire.gen.ts";

/** A held token this close to its expiry is not handed out: it is renewed. */
export const VISITOR_USABLE_SLACK_MS = 60_000;
/** A token is renewed this long before it expires. */
export const VISITOR_RENEW_BEFORE_MS = 60 * 60_000;
/** Never renew sooner than this (a skewed clock must not spin). */
export const VISITOR_MIN_RENEW_MS = 30_000;
/** A failed renewal is retried after this long. */
export const VISITOR_RETRY_MS = 60_000;
/** setTimeout's largest delay. */
const MAX_TIMER_MS = 2_147_483_647;

/** fnv1a64 is a stable 64-bit hash (hex) of s. */
function fnv1a64(s: string): string {
  let h = 0xcbf29ce484222325n;
  for (const b of new TextEncoder().encode(s)) {
    h ^= BigInt(b);
    h = (h * 0x100000001b3n) & 0xffffffffffffffffn;
  }
  return h.toString(16).padStart(16, "0");
}

/** The storage key of one share's visitor token. It names the share by a
 *  hash of its token, never the token itself: a stored visitor token is of
 *  no use without the link it was minted for. */
export function visitorStorageKey(shareToken: string): string {
  return keyFor(StorageKey.appVisitor, fnv1a64(shareToken));
}

/** A stored visitor token, or null when the value is not one. */
export function parseStoredVisitor(raw: string): AppVisitorToken | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as Partial<AppVisitorToken> | null;
    if (v && typeof v.token === "string" && v.token && typeof v.expiresAtMs === "number" && v.expiresAtMs > 0) {
      return { token: v.token, expiresAtMs: v.expiresAtMs };
    }
  } catch {
    /* not JSON */
  }
  return null;
}

/** Whether a held token may be handed to the frame now. */
export function visitorUsable(t: AppVisitorToken | null, nowMs: number): t is AppVisitorToken {
  return !!t && t.expiresAtMs - VISITOR_USABLE_SLACK_MS > nowMs;
}

/** Milliseconds until a token expiring at expiresAtMs is renewed. */
export function visitorRenewDelay(expiresAtMs: number, nowMs: number): number {
  return Math.min(MAX_TIMER_MS, Math.max(VISITOR_MIN_RENEW_MS, expiresAtMs - VISITOR_RENEW_BEFORE_MS - nowMs));
}

/** Whether data is the frame's handshake request (anything else, malformed
 *  or another version, is ignored). */
export function isVisitorRequest(data: unknown): boolean {
  if (!data || typeof data !== "object") return false;
  const m = data as { type?: unknown; v?: unknown };
  return m.type === AppVisitorRequest && m.v === AppHandshakeVersion;
}

/** The grant message carrying token to the frame. */
export function visitorGrant(token: string): { type: string; v: number; token: string } {
  return { type: AppVisitorGrant, v: AppHandshakeVersion, token };
}

/** Where a grant goes: the app frame's window. The frame's origin is
 *  opaque, so the target origin is "*" and the window is what is checked. */
export interface VisitorFrame {
  postMessage(message: unknown, targetOrigin: string): void;
}

export interface VisitorHostDeps {
  /** The share token of the page (scopes the stored token). */
  readonly shareToken: string;
  /** Mints a token for the link; prev renews it for the same visitor. */
  readonly mint: (prev: string | undefined) => Promise<AppVisitorToken>;
  /** The app frame's current window (null before it mounts). */
  readonly frame: () => VisitorFrame | null;
  /** Defaults: localStorage under visitorStorageKey, Date.now, the global
   *  timers. */
  readonly store?: { read(): string; write(value: string): void };
  readonly now?: () => number;
  readonly setTimer?: (fn: () => void, ms: number) => unknown;
  readonly clearTimer?: (handle: unknown) => void;
}

export interface VisitorHost {
  /** Handles a window message event (only the frame's request counts). */
  onMessage(ev: { readonly source: unknown; readonly data: unknown }): void;
  /** Stops renewing (the page left). */
  stop(): void;
}

/** createVisitorHost holds one share's visitor token for its app frame. */
export function createVisitorHost(deps: VisitorHostDeps): VisitorHost {
  const key = visitorStorageKey(deps.shareToken);
  const store = deps.store ?? { read: () => readStored(key), write: (v: string) => writeStored(key, v) };
  const now = deps.now ?? Date.now;
  const setTimer = deps.setTimer ?? ((fn: () => void, ms: number) => setTimeout(fn, ms));
  const clearTimer = deps.clearTimer ?? ((h: unknown) => clearTimeout(h as ReturnType<typeof setTimeout>));

  let held = parseStoredVisitor(store.read());
  let inflight: Promise<AppVisitorToken> | null = null;
  let timer: unknown = null;
  let stopped = false;
  // The token the frame was last given: a second request for the same one
  // means the gateway refused it (a rotated signing key, a skewed clock),
  // so the next answer is a renewed token.
  let granted = "";

  const schedule = (fn: () => void, ms: number) => {
    if (timer !== null) clearTimer(timer);
    timer = stopped ? null : setTimer(fn, ms);
  };

  const push = (t: AppVisitorToken) => {
    const f = deps.frame();
    if (!f || stopped) return;
    granted = t.token;
    f.postMessage(visitorGrant(t.token), "*");
  };

  // mintNow mints (renews a still-valid held token), once at a time, keeps
  // the result and schedules its renewal.
  const mintNow = (): Promise<AppVisitorToken> => {
    if (inflight) return inflight;
    const prev = held && held.expiresAtMs > now() ? held.token : undefined;
    const before = held?.expiresAtMs ?? 0;
    inflight = deps
      .mint(prev)
      .then((t) => {
        held = t;
        store.write(JSON.stringify(t));
        // A renewal that did not extend the token (the link ends first)
        // is not repeated: the token simply ends with the link.
        if (t.expiresAtMs > before) schedule(renew, visitorRenewDelay(t.expiresAtMs, now()));
        return t;
      })
      .finally(() => {
        inflight = null;
      });
    return inflight;
  };

  const renew = () => {
    if (stopped) return;
    mintNow().then(push, () => {
      if (held && held.expiresAtMs > now()) schedule(renew, VISITOR_RETRY_MS);
    });
  };

  // answer gives the frame a usable token: the held one, or a fresh one
  // when there is none or the frame was already given it.
  const answer = async (): Promise<AppVisitorToken> => {
    if (visitorUsable(held, now()) && held.token !== granted) {
      if (timer === null) schedule(renew, visitorRenewDelay(held.expiresAtMs, now()));
      return held;
    }
    try {
      return await mintNow();
    } catch (e) {
      if (visitorUsable(held, now())) return held;
      throw e;
    }
  };

  return {
    onMessage(ev) {
      if (stopped || !isVisitorRequest(ev.data)) return;
      const f = deps.frame();
      if (!f || ev.source !== f) return;
      answer().then(
        (t) => {
          // Only the frame that asked, if it is still the app's frame.
          if (deps.frame() === f) push(t);
        },
        () => {
          /* the SDK times out and the app's call fails; the next request tries again */
        },
      );
    },
    stop() {
      stopped = true;
      if (timer !== null) clearTimer(timer);
      timer = null;
    },
  };
}
