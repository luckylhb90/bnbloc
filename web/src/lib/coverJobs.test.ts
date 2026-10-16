import { createCoverJobs } from "./coverJobs.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// One at a time, in the order asked.
{
  const jobs = createCoverJobs({ slots: 1 });
  jobs.want("a");
  jobs.want("b");
  jobs.want("a");
  eq([jobs.isRunning("a"), jobs.isRunning("b")], [true, false], "the first runs, the second waits; asking again changes nothing");
  jobs.release("a");
  eq([jobs.isRunning("a"), jobs.isRunning("b")], [false, true], "finishing one starts the next");
  jobs.release("b");
  eq([jobs.isRunning("a"), jobs.isRunning("b")], [false, false], "idle");
}

// A version is tried once per session, even if it is asked for again.
{
  const jobs = createCoverJobs();
  jobs.want("a");
  jobs.release("a");
  jobs.want("a");
  eq(jobs.isRunning("a"), false, "a version already tried is not started again");
}

// A card that scrolled away before its turn is not started.
{
  const jobs = createCoverJobs();
  jobs.want("a");
  jobs.want("b");
  jobs.release("b");
  jobs.release("a");
  eq([jobs.isRunning("a"), jobs.isRunning("b")], [false, false], "a released waiter never runs");
}

// The session's total is bounded.
{
  const jobs = createCoverJobs({ maxJobs: 2 });
  for (const k of ["a", "b", "c"]) {
    jobs.want(k);
    jobs.release(k);
  }
  jobs.want("d");
  eq(jobs.isRunning("d"), false, "past the session's limit nothing starts");
}

// A job that does not finish is stopped, and the next one starts.
{
  const jobs = createCoverJobs({ timeoutMs: 40 });
  jobs.want("a");
  jobs.want("b");
  await sleep(55);
  eq([jobs.isRunning("a"), jobs.isRunning("b")], [false, true], "the timeout frees the slot");
  jobs.release("b");
}

// Listeners hear every change, and stop when unsubscribed.
{
  const jobs = createCoverJobs();
  let n = 0;
  const off = jobs.subscribe(() => n++);
  jobs.want("a");
  jobs.release("a");
  const heard = n;
  off();
  jobs.want("b");
  jobs.release("b");
  eq([heard > 0, n === heard], [true, true], "subscribed, then not");
}
console.log("coverJobs tests passed");
