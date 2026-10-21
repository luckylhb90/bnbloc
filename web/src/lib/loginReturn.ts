// The login return path: where signing in brings a person back to, the one
// owner (boundary rule A2; docs/prd/2026-10-13-share-link-sign-in.md).
//
// A share link that requires sign-in to its organization opens the share
// page without a session; the page remembers itself here and sends the
// person to the app's sign-in (the organization's SSO, or direct sign-in
// where the gateway offers it). Whatever way they sign in — the SSO
// callback, a desktop hand-off, the form — the app takes the return once
// it has a session and goes back to the share page, which then mints its
// share ticket. The return stays in this tab's sessionStorage (it survives
// the trip to the identity provider and back, and never travels through
// it), expires, and is only ever a share page's hash or a room deep link
// (a notification's or a shared card's link: `roomDeepLinkHash`, canonical).
//
// Pure apart from the injected store; unit-tested under node.

import { appRoot, roomDeepLinkHash, shareHash, shareTokenFromHash } from "./hashRoute.ts";
import { readStored, StorageKey, writeStored } from "./storageKeys.ts";

/** A remembered return older than this is dropped (a sign-in abandoned
 *  then, and a later one in the same tab, goes home). */
export const LOGIN_RETURN_TTL_MS = 15 * 60_000;

/** Where the return is kept (sessionStorage in the browser). */
export interface ReturnStore {
  read(): string;
  write(value: string): void;
}

const sessionStore: ReturnStore = {
  read: () => readStored(StorageKey.loginReturn, "session"),
  write: (value) => writeStored(StorageKey.loginReturn, value, "session"),
};

/** beginShareSignIn remembers the share page of token and answers the URL
 *  the page sends the person to: the app's sign-in, i.e. the app's
 *  document (pageUrl without its fragment). Null for a token no share page
 *  would take (nothing is remembered). */
export function beginShareSignIn(token: string, pageUrl: string, nowMs: number, store: ReturnStore = sessionStore): string | null {
  const hash = shareHash(token);
  if (shareTokenFromHash(hash) === null) return null;
  store.write(JSON.stringify({ hash, atMs: nowMs }));
  return appRoot(pageUrl);
}

/** beginDeepLinkSignIn remembers the room deep link in hash (the page's
 *  current hash, when a card or a notification opened it without a session)
 *  before the person leaves for the identity provider. Nothing is remembered
 *  for any other hash. */
export function beginDeepLinkSignIn(hash: string, nowMs: number, store: ReturnStore = sessionStore): void {
  const canonical = roomDeepLinkHash(hash);
  if (canonical !== null) store.write(JSON.stringify({ hash: canonical, atMs: nowMs }));
}

/** loginReturnHash is the hash a return may name, canonical: a share page or
 *  a room deep link; null for anything else. */
function loginReturnHash(hash: string): string | null {
  return shareTokenFromHash(hash) !== null ? hash : roomDeepLinkHash(hash);
}

/** takeLoginReturn is the hash to go back to after sign-in (a share page or
 *  a room deep link), at most once (it is cleared), or null: nothing
 *  remembered, expired, or neither. */
export function takeLoginReturn(nowMs: number, store: ReturnStore = sessionStore): string | null {
  const raw = store.read();
  if (!raw) return null;
  store.write("");
  try {
    const v = JSON.parse(raw) as { hash?: unknown; atMs?: unknown };
    if (typeof v.hash !== "string" || typeof v.atMs !== "number") return null;
    if (nowMs - v.atMs > LOGIN_RETURN_TTL_MS || v.atMs > nowMs + 60_000) return null;
    return loginReturnHash(v.hash);
  } catch {
    return null;
  }
}
