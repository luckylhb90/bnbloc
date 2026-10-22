// A window shows one account (docs/prd/2026-10-22-multi-account.md §2.2). The
// binding lives in storage that dies with the window (sessionStorage), so two
// windows show two accounts and a switch re-binds this window only. A new
// window binds to the vault's most recently used account; one opened for a
// purpose says which in its URL hash (an intent, consumed at boot and
// validated against the vault). Pure decisions here; the storage functions
// only read and write the key.

import { StorageKey, readStored, writeStored } from "./storageKeys.ts";
import { accountKey, find, mostRecent, type VaultAccount } from "./vault.ts";

/** Why a window was opened: to sign in another account, or to show one. */
export type Intent = { readonly kind: "add" } | { readonly kind: "account"; readonly key: string };

// The intent rides the hash behind "!" so it never reads as a route (#/...).
const ADD = "#!add-account";
const ACCOUNT = "#!account=";

/** The hash that opens a window for `intent`. */
export function intentHash(intent: Intent): string {
  return intent.kind === "add" ? ADD : ACCOUNT + encodeURIComponent(intent.key);
}

/** The intent a hash carries, and the hash without it. */
export function parseIntent(hash: string): { readonly intent: Intent | null; readonly rest: string } {
  if (hash === ADD) return { intent: { kind: "add" }, rest: "" };
  if (hash.startsWith(ACCOUNT)) {
    try {
      const key = decodeURIComponent(hash.slice(ACCOUNT.length));
      return { intent: key ? { kind: "account", key } : null, rest: "" };
    } catch {
      return { intent: null, rest: "" };
    }
  }
  return { intent: null, rest: hash };
}

export interface Binding {
  /** The account the window shows; null: none (sign in). */
  readonly key: string | null;
  /** The window was opened to sign in another account. */
  readonly adding: boolean;
}

/**
 * What a booting window shows: the intent it was opened for, else the account
 * it was already bound to (a reload), else the account used last. An intent or
 * a stored binding that names an account the vault no longer has (or whose
 * credential was refused) counts for nothing, and a window never shows an
 * account that cannot sign in.
 */
export function resolveBinding(input: {
  readonly stored: string | null;
  readonly intent: Intent | null;
  readonly vault: readonly VaultAccount[];
}): Binding {
  const usable = (key: string | null) => {
    const a = find(input.vault, key);
    return a && !a.signedOut ? accountKey(a.gateway, a.record.subject ?? "") : null;
  };
  if (input.intent?.kind === "add") return { key: null, adding: true };
  const wanted = usable(input.intent?.kind === "account" ? input.intent.key : null);
  if (wanted) return { key: wanted, adding: false };
  const stored = usable(input.stored);
  if (stored) return { key: stored, adding: false };
  const latest = mostRecent(input.vault);
  return { key: latest ? accountKey(latest.gateway, latest.record.subject ?? "") : null, adding: false };
}

export function readBinding(): string | null {
  return readStored(StorageKey.windowAccount, "session") || null;
}

export function writeBinding(key: string | null): void {
  writeStored(StorageKey.windowAccount, key ?? "", "session");
}

/** The account this window shows now, as the vault holds it. */
export function boundAccount(vault: readonly VaultAccount[]): VaultAccount | undefined {
  return find(vault, readBinding());
}
