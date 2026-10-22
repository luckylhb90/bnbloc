import { useEffect, useState } from "react";
import { desktop, type AccountStatus } from "./desktop";
import { statusKey } from "./accountStatus";
import { visiblePoll } from "./useVisiblePoll";

// The shell's state of every account, keyed as the vault names them
// (`shellKey`), re-read every few seconds while `active` (a switcher is
// open). Empty outside the desktop shell.
const EVERY_MS = 5000;

export function useAccountsStatus(active: boolean): ReadonlyMap<string, AccountStatus> {
  const [statuses, setStatuses] = useState<ReadonlyMap<string, AccountStatus>>(new Map());
  useEffect(() => {
    if (!active || !desktop.available()) return;
    let stopped = false;
    const read = () =>
      void desktop.accountsStatus().then((list) => {
        if (!stopped) setStatuses(new Map(list.map((s) => [statusKey(s), s])));
      });
    read();
    const stop = visiblePoll(read, EVERY_MS);
    return () => {
      stopped = true;
      stop();
    };
  }, [active]);
  return statuses;
}
