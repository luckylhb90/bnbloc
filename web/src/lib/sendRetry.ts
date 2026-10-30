// A chat send that meets a moment of trouble (a realtime replica stopping: a
// network error or a 5xx) is sent again, with the same clientMsgId, so the
// server stores it once (docs/production-deployment.md#high-availability).
// A 503 is the database failing over (tens of seconds): it is sent again for
// about 30 s more. Anything else is the server's answer and is not retried.

import { ApiError } from "./errors.ts";

/** The waits before each new attempt. */
export const SEND_RETRY_WAITS: readonly number[] = [400, 1200, 3000];

/** The waits after those while the server answers 503 (about 30 s more). */
export const SEND_UNAVAILABLE_WAITS: readonly number[] = [5000, 8000, 8000, 8000];

/** Whether err is worth sending again: no answer, or a 5xx. */
export function retryableSend(err: unknown): boolean {
  if (err instanceof ApiError) return err.status === 0 || err.status >= 500;
  return true; // a network failure (fetch rejected)
}

function unavailable(err: unknown): boolean {
  return err instanceof ApiError && err.status === 503;
}

/** Runs send, again after each of waits while it fails retryably, and after
 * each of unavailableWaits too while the failure is a 503. */
export async function withSendRetry<T>(
  send: () => Promise<T>,
  waits: readonly number[] = SEND_RETRY_WAITS,
  sleep: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms)),
  unavailableWaits: readonly number[] = SEND_UNAVAILABLE_WAITS,
): Promise<T> {
  for (let i = 0; ; i++) {
    try {
      return await send();
    } catch (e) {
      if (!retryableSend(e)) throw e;
      const wait = i < waits.length ? waits[i] : unavailable(e) ? unavailableWaits[i - waits.length] : undefined;
      if (wait === undefined) throw e;
      await sleep(wait);
    }
  }
}
