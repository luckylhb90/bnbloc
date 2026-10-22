// The renewal of one account's session, free of React so the decisions are
// unit tested (renewal.test.ts). Every window that holds an account renews it
// from the vault's record under one lock per account (withAccountLock), so two
// windows never rotate one refresh token at once: a window that finds the
// record already renewed by a sibling adopts that and mints nothing
// (docs/prd/2026-10-22-multi-account.md §2.1, pre-mortem 3).

import {
  credentialMint,
  errorStatus,
  recordFromGrant,
  renewDelayMs,
  type Grant,
  type MintDeps,
  type SessionRecord,
} from "./sessionGrant.ts";

export type Renewal =
  /** The record is current: a sibling window renewed it, or it is not due. Nothing was minted. */
  | { readonly outcome: "fresh"; readonly record: SessionRecord }
  | { readonly outcome: "renewed"; readonly record: SessionRecord }
  /** The credential was refused: the account is marked signed out. */
  | { readonly outcome: "refused" }
  /** No answer (the network, a 5xx): the record stays and a later call retries. */
  | { readonly outcome: "failed" }
  /** The account has no record or no credential to renew from. */
  | { readonly outcome: "none" };

export interface RenewIo {
  /** The account's record in the vault now. */
  read(): SessionRecord | null;
  /** Keeps a renewed record in the vault (the account is signed in again). */
  write(record: SessionRecord): void;
  markSignedOut(): void;
  now(): number;
  mintDeps: MintDeps;
}

function due(record: SessionRecord, now: number): boolean {
  return renewDelayMs(record.token, now) !== null ? renewDelayMs(record.token, now) === 0 : true;
}

/**
 * Renews the account from the vault's record. `have` is the bearer the caller
 * holds and found refused or due (a window): a record whose bearer is another
 * one and is not due was renewed by a sibling, so it is adopted. Without
 * `have` (the keep-alive of an account no window of this one shows) the
 * account is renewed only when it is due.
 */
export async function renewAccount(io: RenewIo, have: string | null): Promise<Renewal> {
  const record = io.read();
  if (!record) return { outcome: "none" };
  const now = io.now();
  const isDue = due(record, now);
  if (have === null ? !isDue : record.token !== have && !isDue) return { outcome: "fresh", record };
  const mint: (() => Promise<Grant>) | null = credentialMint(record, io.mintDeps);
  if (!mint) return { outcome: "none" };
  try {
    const next = recordFromGrant(await mint());
    io.write(next);
    return { outcome: "renewed", record: next };
  } catch (e) {
    const status = errorStatus(e);
    if (status === 401 || status === 403) {
      io.markSignedOut();
      return { outcome: "refused" };
    }
    return { outcome: "failed" };
  }
}

/**
 * Runs `fn` while holding the account's renewal lock (one per account across
 * every window of the browser); where the Web Locks API is absent it just
 * runs.
 */
export async function withAccountLock<T>(key: string, fn: () => Promise<T>): Promise<T> {
  const locks = typeof navigator !== "undefined" ? navigator.locks : undefined;
  if (!locks) return fn();
  return (await locks.request(`account-renewal:${key}`, fn)) as T;
}
