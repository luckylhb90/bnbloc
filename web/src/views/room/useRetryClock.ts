import { useState } from "react";
import type { AgentJob } from "@/lib/api";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { retryWaiting } from "@/views/room/agentJobLabels";

// useRetryClock is the time a job indicator renders "retrying in Ns"
// against: it re-renders the caller once a second, while the page is
// visible, only while one of jobs is still inside its retry delay.
export function useRetryClock(jobs: readonly AgentJob[]): number {
  const [, setTick] = useState(0);
  const now = Date.now();
  useVisiblePoll(() => setTick((t) => t + 1), 1000, jobs.some((j) => retryWaiting(j, now)));
  return now;
}
