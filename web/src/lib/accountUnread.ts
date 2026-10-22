// What the account switcher says of the accounts no window shows: how many of
// their rooms have unread messages (GET /v1/my/unread: one request each, a
// bounded query on the server). The decisions are pure and unit tested; the
// read composes the account's own renewal, gateway and bearer.

import { errorStatus, renewDelayMs } from "./sessionGrant.ts";
import type { UnreadSummary } from "./wire.gen.ts";
import { keyOf, type VaultAccount } from "./vault.ts";

/** How often the accounts no window shows are asked, while a window is visible. */
export const UNREAD_EVERY_MS = 60_000;
/** An answer is reused this long, so reopening a switcher asks nothing. */
export const UNREAD_FRESH_MS = 30_000;

/** The accounts worth asking: not this window's, still able to sign in. */
export function accountsToAsk(accounts: readonly VaultAccount[], current: string | null): string[] {
  return accounts.filter((a) => !a.signedOut && keyOf(a) !== current).map(keyOf);
}

/** The badge of a summary: "" for none, "3", or "99+" when the number is a floor. */
export function unreadBadge(summary: UnreadSummary | undefined): string {
  if (!summary || summary.rooms <= 0) return "";
  return summary.more ? `${summary.rooms}+` : String(summary.rooms);
}

/** Whether any of the answers has unread rooms: what the account button shows as a dot. */
export function anyUnread(summaries: ReadonlyMap<string, UnreadSummary>): boolean {
  for (const s of summaries.values()) if (s.rooms > 0) return true;
  return false;
}

/** Whether an answer taken at `at` is still fresh at `now`. */
export function isFresh(at: number, now: number): boolean {
  return now - at < UNREAD_FRESH_MS;
}

export interface UnreadReader {
  /** The account's bearer and gateway now (None: it left the vault). */
  account(key: string): { readonly gateway: string; readonly token: string } | null;
  /** The summary on the account's gateway as the bearer. */
  summary(gateway: string, token: string): Promise<UnreadSummary>;
  /** Renews the account (its lock, its gateway); true when it holds a usable bearer after. */
  renew(key: string, have: string): Promise<boolean>;
  now(): number;
}

/**
 * Reads one account's summary. A bearer about to end is renewed first, and a
 * 401 renews and asks once more; any other failure is the answer "unknown"
 * (null), never an error to show: a badge that cannot be had is no badge.
 */
export async function readUnread(io: UnreadReader, key: string): Promise<UnreadSummary | null> {
  let account = io.account(key);
  if (!account) return null;
  if (renewDelayMs(account.token, io.now()) === 0) {
    if (!(await io.renew(key, account.token))) return null;
    account = io.account(key);
    if (!account) return null;
  }
  try {
    return await io.summary(account.gateway, account.token);
  } catch (e) {
    if (errorStatus(e) !== 401) return null;
  }
  if (!(await io.renew(key, account.token))) return null;
  const again = io.account(key);
  if (!again) return null;
  try {
    return await io.summary(again.gateway, again.token);
  } catch {
    return null;
  }
}
