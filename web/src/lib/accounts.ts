// The accounts one browser holds besides the signed-in one
// (docs/prd/2026-10-22-multi-account.md). The signed-in account stays in
// the session key (every reader of it is unchanged); the others are parked
// here, each as the session record it last had, on the gateway it belongs to.
// This module is the ONE owner of the list: the pure decisions below are unit
// tested; the thin storage functions only read and write the key.

import type { SessionRecord } from "./sessionGrant.ts";
import { parseSessionRecord } from "./sessionGrant.ts";
import { StorageKey, readStored, writeStored } from "./storageKeys.ts";

/** A parked account: its last session record and the gateway it is for. */
export interface Account {
  readonly gateway: string;
  readonly record: SessionRecord;
}

/** At most this many accounts are parked (a handful; not an unbounded store). */
export const MAX_PARKED_ACCOUNTS = 8;

/** The key an account is known by: its gateway and subject. */
export function accountKey(gateway: string, subject: string): string {
  return `${gateway}\n${subject}`;
}

export function keyOf(account: Account): string {
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
function renewable(record: SessionRecord): boolean {
  return !!record.subject && !!record.token && (!!record.refreshToken || !!record.apiToken);
}

/** Reads the stored list; whatever cannot be parsed or renewed is dropped. */
export function parseAccounts(raw: string | null | undefined): Account[] {
  if (!raw) return [];
  let list: unknown;
  try {
    list = JSON.parse(raw);
  } catch {
    return [];
  }
  if (!Array.isArray(list)) return [];
  const out: Account[] = [];
  for (const item of list) {
    if (!item || typeof item !== "object") continue;
    const gateway = (item as { gateway?: unknown }).gateway;
    const record = parseSessionRecord(JSON.stringify((item as { record?: unknown }).record ?? null));
    if (typeof gateway !== "string" || !record || !renewable(record)) continue;
    if (!out.some((a) => keyOf(a) === accountKey(gateway, record.subject ?? ""))) out.push({ gateway, record });
  }
  return out.slice(0, MAX_PARKED_ACCOUNTS);
}

/** Parks record (replacing the parked account of the same gateway and subject). */
export function park(list: readonly Account[], gateway: string, record: SessionRecord): Account[] {
  if (!renewable(record)) return [...list];
  const key = accountKey(gateway, record.subject ?? "");
  const rest = list.filter((a) => keyOf(a) !== key);
  return [{ gateway, record }, ...rest].slice(0, MAX_PARKED_ACCOUNTS);
}

/** The list without the account of gateway and subject. */
export function unpark(list: readonly Account[], gateway: string, subject: string | null): Account[] {
  const key = accountKey(gateway, subject ?? "");
  return list.filter((a) => keyOf(a) !== key);
}

/** The parked accounts of one gateway: the ones this app can switch to. */
export function accountsOn(list: readonly Account[], gateway: string): Account[] {
  return list.filter((a) => a.gateway === gateway);
}

/**
 * Whether a change of the stored session (a storage event of another window)
 * means this window now shows the wrong account: the subject differs. A
 * renewal of the same account changes the bearer only and reloads nothing.
 */
export function sessionSubjectChanged(oldRaw: string | null, newRaw: string | null): boolean {
  const subjectOf = (raw: string | null) => parseSessionRecord(raw)?.subject ?? null;
  return subjectOf(oldRaw) !== subjectOf(newRaw);
}

export function readAccounts(): Account[] {
  return parseAccounts(readStored(StorageKey.accounts) || null);
}

export function writeAccounts(list: readonly Account[]): void {
  writeStored(StorageKey.accounts, list.length ? JSON.stringify(list) : "");
}
