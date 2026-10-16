import { useEffect, useSyncExternalStore } from "react";
import { createCoverJobs } from "./coverJobs.ts";

// The session's one queue of covers to make (lib/coverJobs): a card calls this
// with its version's key and whether it still needs a cover, and is told when
// it is its turn to have the file rendered out of sight.
const jobs = createCoverJobs();

export function useCoverJob(key: string, wanted: boolean): boolean {
  useEffect(() => {
    if (!wanted) return;
    jobs.want(key);
    return () => jobs.release(key);
  }, [key, wanted]);
  return useSyncExternalStore(jobs.subscribe, () => wanted && jobs.isRunning(key));
}
