import { appWritesToday, changesBetween, deliverablesToday, freshness, summaryText, tokenRatePerMin, type OrgPulseSnapshot } from "./orgPulse.ts";
import { parseSse, readSse } from "./orgPulseStream.ts";

const ok = (cond: boolean, msg: string) => {
  if (!cond) throw new Error(msg);
};

function snap(over: Partial<OrgPulseSnapshot["today"]> = {}, live: Partial<OrgPulseSnapshot["live"]> = {}): OrgPulseSnapshot {
  return {
    v: 1,
    atMs: 1_000_000,
    settlingSinceMs: 700_000,
    dataAsOfMs: 999_000,
    today: {
      replies: 0, repliesReported: 0, coverage: null, repliesByCore: {}, tokensIn: 0, tokensOut: 0, costUsd: 0,
      jobsClaimed: 0, jobsFinished: 0, messages: 0, artifactVersions: 0, appWrites: 0, ...over,
    },
    live: { agentsWorking: 0, agentsWaiting: 0, oldestWaitingSec: null, peopleOnline: null, peopleSuppressed: true, ...live },
    week: { activePeople: null, activePeopleSuppressed: true },
    geo: { countries: [], countriesWithheld: 0 },
  };
}

// Deliverables are artifact versions; app writes are a separate activity.
ok(deliverablesToday(snap({ artifactVersions: 3, appWrites: 2 })) === 3, "deliverables are the versions only");
ok(appWritesToday(snap({ artifactVersions: 3, appWrites: 2 })) === 2, "app writes are counted on their own");

// Freshness: never written, live, delayed.
const noneSnap = { ...snap(), dataAsOfMs: 0 };
ok(freshness(noneSnap, 1_000_000).state === "none", "a rollup never written is 'none', not live");
ok(freshness(snap(), 1_000_000).state === "live", "a recent write is live");
const old = freshness({ ...snap(), dataAsOfMs: 1_000 }, 200_000);
ok(old.state === "delayed" && old.behindSec === 199, "an old write is delayed with its age");

// Token rate: null until the window is wide enough; then a per-minute rate; never negative.
ok(tokenRatePerMin([], 1000) === null, "no samples, no rate");
ok(tokenRatePerMin([{ t: 0, tokens: 0 }, { t: 1000, tokens: 100 }], 1000) === null, "under five seconds, no rate");
const rate = tokenRatePerMin([{ t: 0, tokens: 0 }, { t: 30_000, tokens: 600 }], 30_000);
ok(rate === 1200, `600 tokens in 30 s is 1200 per minute, got ${rate}`);
ok(tokenRatePerMin([{ t: 0, tokens: 500 }, { t: 30_000, tokens: 100 }], 30_000)! === 0, "a counter that fell reads as zero, not negative");

// Changes: positive deltas only; a rollover (a counter that fell) reads as no change.
const before = snap({ replies: 2, messages: 10, repliesByCore: { codex: { replies: 2, reported: 2 } }, jobsFinished: 4, artifactVersions: 1 }, { agentsWorking: 1 });
const after = snap({ replies: 3, messages: 14, repliesByCore: { codex: { replies: 3, reported: 2 } }, jobsFinished: 4, artifactVersions: 2 }, { agentsWorking: 0 });
const changes = changesBetween(before, after);
ok(changes.includes("+1 agent reply · codex"), "a new reply is named by its core");
ok(changes.includes("+4 messages"), "new messages are counted");
ok(changes.includes("+1 deliverable"), "a new deliverable is counted");
ok(changes.includes("agents working 1 → 0"), "a change in agents working is shown");
const rollover = changesBetween(after, before);
ok(!rollover.some((c) => c.includes("-")), "a counter that went down never shows a negative change");

// The summary is built from the numbers, and withholds small people counts.
const text = summaryText(snap({ replies: 1, repliesReported: 1, coverage: 1, tokensIn: 13690, tokensOut: 4, costUsd: 0.0098, jobsClaimed: 1, jobsFinished: 2, messages: 4502, artifactVersions: 4 }));
ok(text.includes("1 agent reply, 100.0% with usage reported"), "summary carries the coverage");
ok(text.includes("fewer than 5 people were active this week"), "summary withholds a small week count");

// SSE parsing: complete events, a split block completed by the next chunk, and blocks without data.
const a = parseSse("event: snapshot\ndata: {\"v\":1}\n\nevent: error\ndata: {}\n\n");
ok(a.events.length === 2 && a.events[0].event === "snapshot" && a.events[0].data === "{\"v\":1}", "two complete events parse");
ok(a.rest === "", "nothing left over after complete events");
const split = parseSse("event: snap");
ok(split.events.length === 0 && split.rest === "event: snap", "an unfinished block waits for the next chunk");
const joined = parseSse(split.rest + "shot\ndata: 42\n\n");
ok(joined.events[0].event === "snapshot" && joined.events[0].data === "42", "a block split across chunks is completed");
ok(parseSse(": keepalive\n\n").events.length === 0, "a comment-only block is not an event");
ok(parseSse("data: a\ndata: b\n\n").events[0].data === "a\nb", "multi-line data joins with newlines");

// readSse over a real stream, split at an arbitrary byte boundary.
const enc = new TextEncoder();
const body = new ReadableStream<Uint8Array>({
  start(c) {
    const bytes = enc.encode("event: snapshot\ndata: {\"n\":1}\n\nevent: snapshot\ndata: {\"n\":2}\n\n");
    c.enqueue(bytes.slice(0, 7));
    c.enqueue(bytes.slice(7, 30));
    c.enqueue(bytes.slice(30));
    c.close();
  },
});
const got: string[] = [];
await readSse(body, (e) => got.push(e.data));
ok(got.length === 2 && got[0] === "{\"n\":1}" && got[1] === "{\"n\":2}", "readSse delivers every event across chunk boundaries");
