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
// it), expires, and is only ever a share page's hash.
//
// Pure apart from the injected store; unit-tested under node.

import { appRoot, shareHash, shareTokenFromHash } from "./hashRoute.ts";
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

/** takeLoginReturn is the share page hash to go back to after sign-in, at
 *  most once (it is cleared), or null: nothing remembered, expired, or not
 *  a share page. */
export function takeLoginReturn(nowMs: number, store: ReturnStore = sessionStore): string | null {
  const raw = store.read();
  if (!raw) return null;
  store.write("");
  try {
    const v = JSON.parse(raw) as { hash?: unknown; atMs?: unknown };
    if (typeof v.hash !== "string" || typeof v.atMs !== "number") return null;
    if (nowMs - v.atMs > LOGIN_RETURN_TTL_MS || v.atMs > nowMs + 60_000) return null;
    return shareTokenFromHash(v.hash) === null ? null : v.hash;
  } catch {
    return null;
  }
}
