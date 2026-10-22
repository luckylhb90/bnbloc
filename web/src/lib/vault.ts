// The vault: every account this app holds, in one list
// (docs/prd/2026-10-22-multi-account.md §2.1). Each is signed in at the same
// time: a window is bound to one of them (lib/windowBinding.ts) and shows only
// that one; the others stay in the vault, renewed, and (on the desktop) live
// in the shell. This module is the ONE owner of the list: the pure decisions
// are unit tested, the thin storage functions only read and write the key.

import type { SessionRecord } from "./sessionGrant.ts";
import { parseSessionRecord } from "./sessionGrant.ts";
import { StorageKey, readStored, writeStored } from "./storageKeys.ts";

/** An account of the vault: its last session record on the gateway it belongs to. */
export interface VaultAccount {
  readonly gateway: string;
  readonly record: SessionRecord;
  /** When a window last bound to it (ms): a new window binds to the latest. */
  readonly lastUsed: number;
  /** The hash route a window restores when it binds to it ("" = none). */
  readonly lastView: string;
  /** Its credential was refused: it shows as "sign in again", never vanishes. */
  readonly signedOut: boolean;
}

/** At most this many accounts (a handful; not an unbounded store). */
export const MAX_ACCOUNTS = 8;

/** The key an account is known by: its gateway and subject. */
export function accountKey(gateway: string, subject: string): string {
  return `${gateway}\n${subject}`;
}

/** The gateway and subject an account key names (accountKey's inverse). */
export function parseKey(key: string): { readonly gateway: string; readonly subject: string } | null {
  const at = key.indexOf("\n");
  return at < 0 ? null : { gateway: key.slice(0, at), subject: key.slice(at + 1) };
}

export function keyOf(account: VaultAccount): string {
  return accountKey(account.gateway, account.record.subject ?? "");
}

/**
 * A test account renews from an API token a super admin issued; any other
 * account is a person who signed in (it renews from a refresh token).
 */
export type AccountKind = "member" | "test";

export function accountKind(record: SessionRecord): AccountKind {
  return record.apiToken ? "test" : "member";
}

/** Whether a record can be renewed at all: it has a credential and a subject. */
export function renewable(record: SessionRecord): boolean {
  return !!record.subject && !!record.token && (!!record.refreshToken || !!record.apiToken);
}

function parseStored(raw: string | null | undefined): { readonly rev: number; readonly list: unknown } {
  if (!raw) return { rev: 0, list: [] };
  try {
    const stored = JSON.parse(raw) as { rev?: unknown; accounts?: unknown };
    return { rev: typeof stored.rev === "number" && Number.isFinite(stored.rev) ? stored.rev : 0, list: stored.accounts };
  } catch {
    return { rev: 0, list: [] };
  }
}

/**
 * The vault's revision: how many times it was written. The desktop shell
 * reconciles to the newest snapshot it was handed and drops an older one (a
 * window's, arriving late), so each snapshot carries the revision it was read
 * at.
 */
export function parseRev(raw: string | null | undefined): number {
  return parseStored(raw).rev;
}

/** Reads the stored list; whatever cannot be parsed or renewed is dropped. */
export function parseVault(raw: string | null | undefined): VaultAccount[] {
  const list = parseStored(raw).list;
  if (!Array.isArray(list)) return [];
  const out: VaultAccount[] = [];
  for (const item of list) {
    if (!item || typeof item !== "object") continue;
    const o = item as Record<string, unknown>;
    const record = parseSessionRecord(JSON.stringify(o.record ?? null));
    if (typeof o.gateway !== "string" || !record || !renewable(record)) continue;
    const account: VaultAccount = {
      gateway: o.gateway,
      record,
      lastUsed: typeof o.lastUsed === "number" && Number.isFinite(o.lastUsed) ? o.lastUsed : 0,
      lastView: typeof o.lastView === "string" ? o.lastView : "",
      signedOut: o.signedOut === true,
    };
    if (!out.some((a) => keyOf(a) === keyOf(account))) out.push(account);
  }
  return out.slice(0, MAX_ACCOUNTS);
}

export function serializeVault(list: readonly VaultAccount[], rev: number): string {
  return list.length || rev > 0 ? JSON.stringify({ rev, accounts: list }) : "";
}

export function find(list: readonly VaultAccount[], key: string | null): VaultAccount | undefined {
  return key === null ? undefined : list.find((a) => keyOf(a) === key);
}

/**
 * Adds an account, or replaces the one of the same gateway and subject (its
 * last view stays): signed in again, used now. When the vault is full the
 * least recently used account that is signed out goes first, else the least
 * recently used; the account being added never does.
 */
export function put(list: readonly VaultAccount[], gateway: string, record: SessionRecord, now: number): VaultAccount[] {
  if (!renewable(record)) return [...list];
  const key = accountKey(gateway, record.subject ?? "");
  const before = find(list, key);
  const next: VaultAccount = { gateway, record, lastUsed: now, lastView: before?.lastView ?? "", signedOut: false };
  const rest = list.filter((a) => keyOf(a) !== key);
  if (rest.length >= MAX_ACCOUNTS) {
    const victim = [...rest].sort((a, b) => Number(b.signedOut) - Number(a.signedOut) || a.lastUsed - b.lastUsed)[0]!;
    rest.splice(rest.indexOf(victim), 1);
  }
  return [next, ...rest];
}

/** Replaces the record of an account that is in the vault (a renewal or a tenant switch); signed in again. */
export function withRecord(list: readonly VaultAccount[], key: string, record: SessionRecord): VaultAccount[] {
  return list.map((a) => (keyOf(a) === key ? { ...a, record, signedOut: false } : a));
}

export function remove(list: readonly VaultAccount[], key: string): VaultAccount[] {
  return list.filter((a) => keyOf(a) !== key);
}

/** A window bound to the account: it is the latest used. */
export function touch(list: readonly VaultAccount[], key: string, now: number): VaultAccount[] {
  return list.map((a) => (keyOf(a) === key ? { ...a, lastUsed: now } : a));
}

export function withView(list: readonly VaultAccount[], key: string, view: string): VaultAccount[] {
  return list.map((a) => (keyOf(a) === key && a.lastView !== view ? { ...a, lastView: view } : a));
}

/** The account's credential was refused: it stays, marked. */
export function markSignedOut(list: readonly VaultAccount[], key: string): VaultAccount[] {
  return list.map((a) => (keyOf(a) === key ? { ...a, signedOut: true } : a));
}

/** The account a new window binds to: the one used last that can still sign in. */
export function mostRecent(list: readonly VaultAccount[]): VaultAccount | undefined {
  return [...list].filter((a) => !a.signedOut).sort((a, b) => b.lastUsed - a.lastUsed)[0];
}

export function readVault(): VaultAccount[] {
  return parseVault(readStored(StorageKey.vault) || null);
}

/** The revision of the stored vault (see `parseRev`). */
export function readVaultRev(): number {
  return parseRev(readStored(StorageKey.vault) || null);
}

/**
 * Reads the vault, applies `change`, writes the result (only when it differs:
 * every write wakes the other windows, and counts as a revision) and returns
 * it.
 */
export function updateVault(change: (list: VaultAccount[]) => VaultAccount[]): VaultAccount[] {
  const raw = readStored(StorageKey.vault) || null;
  const before = parseVault(raw);
  const rev = parseRev(raw);
  const next = change(before);
  if (serializeVault(next, rev) !== serializeVault(before, rev)) writeStored(StorageKey.vault, serializeVault(next, rev + 1));
  return next;
}
