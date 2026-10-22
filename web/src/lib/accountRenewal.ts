// The one renewal of an account of the vault, from anywhere: the 401 recovery
// of the window's own account, the proactive timer, the keep-alive of the
// accounts no window shows, a switch, and a read as another account (the
// unread badge). It joins the vault, the account's own gateway and the renewal
// lock (renewal.ts).

import { api } from "./api";
import { gatewayBase } from "./config";
import { renewAccount, withAccountLock, type RenewIo } from "./renewal";
import { find, markSignedOut, readVault, updateVault, withRecord } from "./vault";

// renewIo is the renewal's view of one account in the vault: its record, and
// the credential calls on ITS gateway (not the window's) with no session
// headers of the window's.
function renewIo(key: string): RenewIo {
  const gateway = find(readVault(), key)?.gateway ?? gatewayBase();
  return {
    read: () => find(readVault(), key)?.record ?? null,
    write: (record) => void updateVault((list) => withRecord(list, key, record)),
    markSignedOut: () => void updateVault((list) => markSignedOut(list, key)),
    now: Date.now,
    mintDeps: {
      refresh: (refreshToken) => api.refresh(refreshToken, gateway),
      exchangeApiToken: (apiToken) => api.exchangeApiToken(apiToken, gateway),
    },
  };
}

// renewUnderLock is the ONE renewal of an account: under its lock, from the
// vault's record, adopting a sibling window's renewal instead of minting again.
export function renewUnderLock(key: string, have: string | null) {
  return withAccountLock(key, () => renewAccount(renewIo(key), have));
}

