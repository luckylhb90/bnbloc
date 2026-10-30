import assert from "node:assert/strict";
import test from "node:test";
import { ApiError } from "./errors.ts";
import { retryableSend, withSendRetry } from "./sendRetry.ts";

const noSleep = async () => {};

test("a send is tried again after a network error or a 5xx, not after a 4xx", async () => {
  let n = 0;
  const got = await withSendRetry(async () => {
    n++;
    if (n < 3) throw n === 1 ? new TypeError("fetch failed") : new ApiError(502, "bad gateway");
    return 7;
  }, [1, 1, 1], noSleep);
  assert.equal(got, 7);
  assert.equal(n, 3);
  n = 0;
  await assert.rejects(withSendRetry(async () => { n++; throw new ApiError(429, "slow down"); }, [1, 1], noSleep));
  assert.equal(n, 1, "a refusal is the server's answer");
  assert.equal(retryableSend(new ApiError(503, "x")), true);
  assert.equal(retryableSend(new ApiError(404, "x")), false);
});

test("after the last wait the failure is the caller's", async () => {
  let n = 0;
  await assert.rejects(withSendRetry(async () => { n++; throw new ApiError(500, "x"); }, [1, 1], noSleep));
  assert.equal(n, 3);
});
