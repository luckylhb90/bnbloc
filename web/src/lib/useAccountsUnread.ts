import { useEffect, useRef, useState } from "react";
import { UNREAD_EVERY_MS, accountsToAsk, isFresh, readUnread } from "./accountUnread";
import { liveReader } from "./accountUnreadLive";
import type { UnreadSummary } from "./wire.gen.ts";
import { visiblePoll } from "./useVisiblePoll";
import type { VaultAccount } from "./vault";

// The unread summary of every account this window does not show, by account
// key. Asked every UNREAD_EVERY_MS while the window is visible (and once at
// once), only when there is another account to ask, one at a time, never an
// answer fresher than UNREAD_FRESH_MS asked again. Empty with one account: the
// window of the account in front reads its own rooms as it always did
// (Shell's unread poll).
export function useAccountsUnread(accounts: readonly VaultAccount[], current: string | null): ReadonlyMap<string, UnreadSummary> {
  const [summaries, setSummaries] = useState<ReadonlyMap<string, UnreadSummary>>(new Map());
  const answered = useRef(new Map<string, { at: number; summary: UnreadSummary | null }>());
  const keys = accountsToAsk(accounts, current);
  const keysId = keys.join("\u0000");
  useEffect(() => {
    if (keys.length === 0) {
      answered.current.clear();
      setSummaries(new Map());
      return;
    }
    let stopped = false;
    const ask = async () => {
      for (const key of keys) {
        if (stopped) return;
        const known = answered.current.get(key);
        if (known && isFresh(known.at, Date.now())) continue;
        answered.current.set(key, { at: Date.now(), summary: await readUnread(liveReader, key) });
      }
      if (stopped) return;
      const next = new Map<string, UnreadSummary>();
      for (const key of keys) {
        const summary = answered.current.get(key)?.summary;
        if (summary) next.set(key, summary);
      }
      for (const key of [...answered.current.keys()]) if (!keys.includes(key)) answered.current.delete(key);
      setSummaries(next);
    };
    void ask();
    const stop = visiblePoll(() => void ask(), UNREAD_EVERY_MS);
    return () => {
      stopped = true;
      stop();
    };
    // keys is derived from keysId.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [keysId]);
  return summaries;
}
