// The accounts the web hands the desktop shell (docs/prd/2026-10-22-multi-
// account.md §2.1): the shell reconciles its account runtimes to this list,
// so an account that is not in it stops serving and its sign-in on the
// gateway ends. Pure and unit tested; desktop.ts sends it.

import type { SessionRecord } from "./sessionGrant.ts";

/** What the shell renews an account from: a person's refresh token (forked
 *  once by the shell into a family of its own), or a test account's API token. */
export type VaultCredential =
  | { readonly kind: "refresh"; readonly token: string }
  | { readonly kind: "apiToken"; readonly token: string };

/** One account of the vault, as the shell reads it. */
export interface VaultAccount {
  readonly gatewayUrl: string;
  readonly subject: string;
  /** The tenant the account acts in ("" = the gateway's default). */
  readonly tenant: string;
  readonly credential: VaultCredential;
}

export interface HeldAccount {
  readonly gateway: string;
  readonly record: SessionRecord;
}

/** The account a record holds, or null when it cannot be renewed (no subject,
 *  no gateway, no credential): the shell is never handed a half account. */
export function vaultAccount(held: HeldAccount): VaultAccount | null {
  const gatewayUrl = held.gateway.trim().replace(/\/+$/, "");
  const subject = held.record.subject?.trim() ?? "";
  if (!gatewayUrl || !subject) return null;
  const credential: VaultCredential | null = held.record.apiToken
    ? { kind: "apiToken", token: held.record.apiToken }
    : held.record.refreshToken
      ? { kind: "refresh", token: held.record.refreshToken }
      : null;
  if (!credential) return null;
  return { gatewayUrl, subject, tenant: held.record.tenant?.trim() ?? "", credential };
}

/** The list handed to the shell: every renewable account once (the first of a
 *  gateway and subject wins). */
export function snapshotOf(held: readonly HeldAccount[]): VaultAccount[] {
  const seen = new Set<string>();
  const out: VaultAccount[] = [];
  for (const h of held) {
    const account = vaultAccount(h);
    if (!account) continue;
    const key = `${account.gatewayUrl}\n${account.subject}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(account);
  }
  return out;
}

/** The error the shell answers a command from a window that shows no account
 *  (src-tauri/src/runtime.rs, `NOT_BOUND`). */
export const NOT_BOUND = "no account is bound to this window";

export function isNotBound(error: unknown): boolean {
  return String(error instanceof Error ? error.message : error).includes(NOT_BOUND);
}
