// The queue for making covers of files nobody has opened
// (docs/prd/2026-10-16-file-covers.md level 3): a card in view whose version
// has no cover may have its viewer render the file once, out of sight, so the
// card gets a picture. Bounded on every side: one job at a time, each at most
// `timeoutMs`, a version tried once per session (a cover that cannot be stored
// is not retried), at most `maxJobs` per session. Pure: no React, no DOM.

export interface CoverJobsOptions {
  /** Jobs running at once. */
  readonly slots?: number;
  /** A job that has not finished by then is stopped. */
  readonly timeoutMs?: number;
  /** Jobs started in the session; past it, nothing more starts. */
  readonly maxJobs?: number;
}

export interface CoverJobs {
  /** Asks for a job (idempotent). */
  want(key: string): void;
  /** The card no longer needs it (the cover arrived, it scrolled away). */
  release(key: string): void;
  isRunning(key: string): boolean;
  subscribe(listener: () => void): () => void;
}

export function createCoverJobs({ slots = 1, timeoutMs = 25_000, maxJobs = 24 }: CoverJobsOptions = {}): CoverJobs {
  const waiting: string[] = [];
  const running = new Map<string, ReturnType<typeof setTimeout>>();
  const tried = new Set<string>();
  const listeners = new Set<() => void>();
  const changed = () => {
    for (const l of listeners) l();
  };

  const pump = () => {
    while (running.size < slots && waiting.length > 0 && tried.size < maxJobs) {
      const key = waiting.shift() as string;
      tried.add(key);
      running.set(
        key,
        setTimeout(() => release(key), timeoutMs),
      );
    }
    changed();
  };

  function release(key: string): void {
    const timer = running.get(key);
    if (timer !== undefined) {
      clearTimeout(timer);
      running.delete(key);
    }
    const queued = waiting.indexOf(key);
    if (queued >= 0) waiting.splice(queued, 1);
    pump();
  }

  return {
    want(key) {
      if (tried.has(key) || running.has(key) || waiting.includes(key)) return;
      waiting.push(key);
      pump();
    },
    release,
    isRunning: (key) => running.has(key),
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}
