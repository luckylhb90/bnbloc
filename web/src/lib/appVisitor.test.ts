import {
  VISITOR_MIN_RENEW_MS,
  VISITOR_RENEW_BEFORE_MS,
  VISITOR_RETRY_MS,
  createVisitorHost,
  isVisitorRequest,
  parseStoredVisitor,
  visitorRenewDelay,
  visitorStorageKey,
  type VisitorFrame,
} from "./appVisitor.ts";
import { AppHandshakeVersion, AppVisitorGrant, AppVisitorRequest, type AppVisitorToken } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const HOUR = 60 * 60_000;
const flush = () => new Promise<void>((r) => setTimeout(r, 0));

// A frame window that records what it was sent.
function frame(): VisitorFrame & { sent: unknown[] } {
  const sent: unknown[] = [];
  return { sent, postMessage: (m: unknown, origin: string) => sent.push({ m, origin }) };
}

// A host over fake storage, clock, timers and mint.
function rig(opts: { stored?: string; share?: string; mintFails?: () => boolean; ttl?: number } = {}) {
  const store = new Map<string, string>();
  let clock = 1_000_000;
  const timers: { fn: () => void; at: number; id: number }[] = [];
  let nextId = 1;
  const mints: (string | undefined)[] = [];
  let f: (VisitorFrame & { sent: unknown[] }) | null = frame();
  const key = visitorStorageKey(opts.share ?? "bsh_share_secret"); // gitleaks:allow
  if (opts.stored) store.set(key, opts.stored);
  const host = createVisitorHost({
    shareToken: opts.share ?? "bsh_share_secret", // gitleaks:allow
    mint: async (prev) => {
      mints.push(prev);
      if (opts.mintFails?.()) throw new Error("mint failed");
      return { token: `vt${mints.length}`, expiresAtMs: clock + (opts.ttl ?? 24 * HOUR) };
    },
    frame: () => f,
    store: { read: () => store.get(key) ?? "", write: (v) => store.set(key, v) },
    now: () => clock,
    setTimer: (fn, ms) => {
      const id = nextId++;
      timers.push({ fn, at: clock + ms, id });
      return id;
    },
    clearTimer: (id) => {
      const i = timers.findIndex((t) => t.id === id);
      if (i >= 0) timers.splice(i, 1);
    },
  });
  return {
    host,
    store,
    key,
    mints,
    timers,
    get frame() {
      return f;
    },
    setFrame(next: (VisitorFrame & { sent: unknown[] }) | null) {
      f = next;
    },
    advance(ms: number) {
      clock += ms;
      for (const t of [...timers].sort((a, b) => a.at - b.at)) {
        if (t.at <= clock) {
          timers.splice(timers.indexOf(t), 1);
          t.fn();
        }
      }
    },
    now: () => clock,
  };
}

const request = { type: AppVisitorRequest, v: AppHandshakeVersion };
const grantsOf = (f: { sent: unknown[] }) =>
  f.sent.map((s) => s as { m: { type: string; v: number; token: string }; origin: string });

async function testAnswersOnlyItsOwnFrame(): Promise<void> {
  const r = rig();
  const own = r.frame!;
  const stranger = frame();
  r.host.onMessage({ source: stranger, data: request });
  r.host.onMessage({ source: null, data: request });
  r.host.onMessage({ source: own, data: { type: AppVisitorRequest, v: 2 } });
  r.host.onMessage({ source: own, data: { type: AppVisitorGrant, v: AppHandshakeVersion } });
  r.host.onMessage({ source: own, data: "bnbloc.app.visitor.request" });
  r.host.onMessage({ source: own, data: null });
  await flush();
  assert(r.mints.length === 0 && own.sent.length === 0 && stranger.sent.length === 0, "other sources and malformed messages are ignored, nothing minted");

  r.host.onMessage({ source: own, data: request });
  await flush();
  const g = grantsOf(own);
  assert(g.length === 1 && r.mints.length === 1, "its own frame's request mints once and is answered");
  assert(g[0].m.type === AppVisitorGrant && g[0].m.v === AppHandshakeVersion && g[0].m.token === "vt1" && g[0].origin === "*", "the grant carries the token");
  assert(stranger.sent.length === 0, "a stranger never gets the token");

  assert(isVisitorRequest(request) && !isVisitorRequest({ type: AppVisitorRequest }) && !isVisitorRequest(undefined), "the request shape is exact");
}

async function testAnswerGoesOnlyToTheFrameThatAsked(): Promise<void> {
  const r = rig();
  const first = r.frame!;
  r.host.onMessage({ source: first, data: request });
  r.setFrame(frame()); // the frame was replaced before the mint answered
  await flush();
  assert(first.sent.length === 0 && r.frame!.sent.length === 0, "a replaced frame's answer is dropped");
}

async function testStoredTokenReusedThenRenewedOnRepeat(): Promise<void> {
  const stored: AppVisitorToken = { token: "kept", expiresAtMs: 1_000_000 + 10 * HOUR };
  const r = rig({ stored: JSON.stringify(stored) });
  const own = r.frame!;
  r.host.onMessage({ source: own, data: request });
  await flush();
  assert(r.mints.length === 0 && grantsOf(own)[0].m.token === "kept", "a returning visitor gets the stored token without a mint");
  // Asked again for the token it already holds: the gateway refused it.
  r.host.onMessage({ source: own, data: request });
  await flush();
  assert(r.mints.length === 1 && r.mints[0] === "kept", "a repeat request renews the held token (same visitor)");
  assert(grantsOf(own)[1].m.token === "vt1", "and answers with the renewed one");
  assert(parseStoredVisitor(r.store.get(r.key)!)?.token === "vt1", "the renewed token is stored");
}

async function testRepeatFallsBackWhenRenewalFails(): Promise<void> {
  const stored: AppVisitorToken = { token: "kept", expiresAtMs: 1_000_000 + 10 * HOUR };
  const r = rig({ stored: JSON.stringify(stored), mintFails: () => true });
  const own = r.frame!;
  r.host.onMessage({ source: own, data: request });
  r.host.onMessage({ source: own, data: request });
  await flush();
  await flush();
  assert(grantsOf(own).every((g) => g.m.token === "kept") && grantsOf(own).length === 2, "a failed renewal still answers with the usable token");
}

async function testExpiredStoredTokenIsNotUsed(): Promise<void> {
  const r = rig({ stored: JSON.stringify({ token: "old", expiresAtMs: 1_000_000 - 1 }) });
  r.host.onMessage({ source: r.frame, data: request });
  await flush();
  assert(r.mints.length === 1 && r.mints[0] === undefined, "an expired token is neither handed out nor sent to renew");
}

async function testRenewalScheduling(): Promise<void> {
  assert(visitorRenewDelay(1_000_000 + 24 * HOUR, 1_000_000) === 24 * HOUR - VISITOR_RENEW_BEFORE_MS, "renews an hour before expiry");
  assert(visitorRenewDelay(1_000_000 + 10_000, 1_000_000) === VISITOR_MIN_RENEW_MS, "never sooner than the floor");
  assert(visitorRenewDelay(Number.MAX_SAFE_INTEGER, 0) === 2_147_483_647, "never past setTimeout's range");

  const r = rig();
  const own = r.frame!;
  assert(r.timers.length === 0, "nothing is scheduled before the first need");
  r.host.onMessage({ source: own, data: request });
  await flush();
  assert(r.timers.length === 1 && r.timers[0].at === r.now() + 24 * HOUR - VISITOR_RENEW_BEFORE_MS, "the renewal is scheduled before expiry");
  r.advance(24 * HOUR - VISITOR_RENEW_BEFORE_MS);
  await flush();
  assert(r.mints.length === 2 && r.mints[1] === "vt1", "the renewal sends the held token");
  const g = grantsOf(own);
  assert(g.length === 2 && g[1].m.token === "vt2", "the renewed grant is pushed to the frame unasked");
  assert(r.timers.length === 1, "and the next renewal is scheduled");

  r.host.stop();
  assert(r.timers.length === 0, "stop cancels the renewal");
}

async function testFailedRenewalRetries(): Promise<void> {
  let fail = false;
  const r = rig({ mintFails: () => fail });
  r.host.onMessage({ source: r.frame, data: request });
  await flush();
  fail = true;
  r.advance(r.timers[0].at - r.now());
  await flush();
  await flush();
  assert(r.timers.length === 1 && r.timers[0].at === r.now() + VISITOR_RETRY_MS, "a failed renewal retries later");
  fail = false;
  r.advance(VISITOR_RETRY_MS);
  await flush();
  assert(grantsOf(r.frame!).length === 2, "the retried renewal pushes its grant");
}

async function testNoExtensionNoRepeat(): Promise<void> {
  // A mint that cannot extend (the link ends first) is not repeated.
  const r = rig();
  const fixedEnd = 1_000_000 + 2 * HOUR;
  const host = createVisitorHost({
    shareToken: "s",
    mint: async () => ({ token: "t", expiresAtMs: fixedEnd }),
    frame: () => r.frame,
    store: { read: () => "", write: () => {} },
    now: r.now,
    setTimer: (fn, ms) => {
      r.timers.push({ fn, at: r.now() + ms, id: 99 });
      return 99;
    },
    clearTimer: () => {
      r.timers.length = 0;
    },
  });
  host.onMessage({ source: r.frame, data: request });
  await flush();
  assert(r.timers.length === 1, "the first token schedules its renewal");
  r.advance(r.timers[0].at - r.now());
  await flush();
  assert(r.timers.length === 0, "a renewal that did not extend the token is not rescheduled");
}

function testStorageKeyScopedPerShare(): void {
  const a = visitorStorageKey("bsh_a_secret-one"); // gitleaks:allow
  const b = visitorStorageKey("bsh_b_secret-two"); // gitleaks:allow
  assert(a === visitorStorageKey("bsh_a_secret-one"), "one share, one key"); // gitleaks:allow
  assert(a !== b, "two shares never share a key");
  assert(a.startsWith("bnbloc.appVisitor:"), `the key is the table's: ${a}`);
  assert(!a.includes("secret-one") && !a.includes("bsh_a"), "the key never holds the share token");

  assert(parseStoredVisitor("") === null && parseStoredVisitor("nope") === null, "garbage is no token");
  assert(parseStoredVisitor(JSON.stringify({ token: "", expiresAtMs: 5 })) === null, "an empty token is none");
  assert(parseStoredVisitor(JSON.stringify({ token: "t", expiresAtMs: "5" })) === null, "a malformed expiry is none");
}

async function testStoredPerShare(): Promise<void> {
  const one = rig({ share: "bsh_one_x" });
  one.host.onMessage({ source: one.frame, data: request });
  await flush();
  assert(one.store.has(visitorStorageKey("bsh_one_x")), "the token is stored under its share's key");
  assert(!one.store.has(visitorStorageKey("bsh_two_y")), "and under no other share's");
}

await testAnswersOnlyItsOwnFrame();
await testAnswerGoesOnlyToTheFrameThatAsked();
await testStoredTokenReusedThenRenewedOnRepeat();
await testRepeatFallsBackWhenRenewalFails();
await testExpiredStoredTokenIsNotUsed();
await testRenewalScheduling();
await testFailedRenewalRetries();
await testNoExtensionNoRepeat();
testStorageKeyScopedPerShare();
await testStoredPerShare();
