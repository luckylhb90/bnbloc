// A chat send that meets a moment of trouble (a realtime replica stopping, a
// room moving to another owner: a network error or a 5xx) is sent again,
// with the same clientMsgId, so the server stores it once
// (docs/production-deployment.md#high-availability). Anything else is the
// server's answer and is not retried.

import { ApiError } from "./errors.ts";

/** The waits before each new attempt. */
export const SEND_RETRY_WAITS: readonly number[] = [400, 1200, 3000];

/** Whether err is worth sending again: no answer, or a 5xx. */
export function retryableSend(err: unknown): boolean {
  if (err instanceof ApiError) return err.status === 0 || err.status >= 500;
  return true; // a network failure (fetch rejected)
}

/** Runs send, again after each of waits while it fails retryably. */
export async function withSendRetry<T>(send: () => Promise<T>, waits: readonly number[] = SEND_RETRY_WAITS, sleep: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms))): Promise<T> {
  for (let i = 0; ; i++) {
    try {
      return await send();
    } catch (e) {
      if (i >= waits.length || !retryableSend(e)) throw e;
      await sleep(waits[i]);
    }
  }
}
