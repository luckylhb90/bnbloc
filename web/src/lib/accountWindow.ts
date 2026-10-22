// Opening a window for an account (docs/prd/2026-10-22-multi-account.md §2.2):
// a browser tab, or a desktop shell window bound to the account. Two accounts
// side by side are two windows.

import { desktop } from "./desktop";
import { intentHash, type Intent } from "./windowBinding.ts";
import { parseKey } from "./vault.ts";

/**
 * Opens a window for `intent` (to sign in another account, or to show one).
 * False when none could be opened (a blocked pop-up): the caller then does it
 * in this window. In a browser the tab is opened before anything is awaited,
 * so it still counts as the click's.
 */
export function openAccountWindow(intent: Intent): Promise<boolean> {
  if (desktop.available()) {
    const account = intent.kind === "account" ? parseKey(intent.key) : null;
    return desktop.openAccountWindow(account?.gateway ?? null, account?.subject ?? null);
  }
  const url = window.location.href.split("#")[0] + intentHash(intent);
  return Promise.resolve(window.open(url, "_blank") !== null);
}
