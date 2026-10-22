import { api } from "./api";
import { renewUnderLock } from "./accountRenewal";
import type { UnreadReader } from "./accountUnread.ts";
import { find, readVault } from "./vault.ts";

/** The reader of the real vault, gateways and renewal. */
export const liveReader: UnreadReader = {
  account: (key) => {
    const a = find(readVault(), key);
    return a ? { gateway: a.gateway, token: a.record.token } : null;
  },
  summary: api.unreadSummaryOf,
  renew: async (key, have) => {
    const r = await renewUnderLock(key, have);
    return r.outcome === "renewed" || r.outcome === "fresh";
  },
  now: Date.now,
};
