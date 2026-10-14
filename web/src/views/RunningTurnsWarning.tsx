import { useEffect, useState } from "react";
import { desktop } from "@/lib/desktop";
import { runningTurnsWarning } from "@/lib/pendingUpdates";
import { useVisiblePoll } from "@/lib/useVisiblePoll";

// Next to an install that restarts the app (its own update): says how many
// agent tasks this machine is running and that installing stops them
// (live turns PRD). Re-read while shown; nothing when none runs.
export function RunningTurnsWarning() {
  const [running, setRunning] = useState(0);
  const read = () => void desktop.bridgeRunningTurns().then(setRunning);
  useEffect(read, []);
  useVisiblePoll(read, 5000);
  const warning = runningTurnsWarning(running);
  return warning ? <p className="text-xs text-[var(--color-warning)]">{warning}</p> : null;
}
