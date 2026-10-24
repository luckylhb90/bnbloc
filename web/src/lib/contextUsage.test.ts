import { contextLevel, contextOf, latestContext } from "./contextUsage.ts";
import { readTurnMetrics } from "./roomEvents.ts";
import type { TurnMetrics } from "./apiModels.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function m(contextTokens: number, contextWindow: number, model?: string): TurnMetrics {
  return {
    ...(model ? { model } : {}),
    reported: true, inputTokens: 1, outputTokens: 1, cacheCreationTokens: 0, cacheReadTokens: 0,
    thinkingTokens: 0, durationApiMs: 0, ttftMs: 0, contextTokens, contextWindow,
  };
}

// The reader: both keys are plain counts, read even when the turn's tokens and
// cost were not reported; anything that is not a positive number is 0.
function testReader(): void {
  const full = readTurnMetrics({ model: "m-1", reported: true, inputTokens: 5, contextTokens: 118_000, contextWindow: 1_000_000 });
  assert(full?.contextTokens === 118_000 && full.contextWindow === 1_000_000, `full ${JSON.stringify(full)}`);
  const unreported = readTurnMetrics({ reported: false, contextTokens: 5000, contextWindow: 20_000 });
  assert(unreported?.reported === false && unreported.contextTokens === 5000, "context is read when billing is not reported");
  assert(unreported?.inputTokens === 0, "the billing counts stay zero when unreported");
  for (const bad of [-1, 0, "5000", null, NaN, Infinity, {}, [5]]) {
    const r = readTurnMetrics({ model: "x", contextTokens: bad, contextWindow: bad });
    assert(r?.contextTokens === 0 && r.contextWindow === 0, `bad value ${String(bad)} read as ${r?.contextTokens}`);
  }
  assert(readTurnMetrics({ contextTokens: 12.9, contextWindow: 100.4 })?.contextTokens === 12, "counts are whole");
}

function testPercentAndLevels(): void {
  assert(contextOf(m(118_000, 1_000_000))?.percent === 11, "11% (rounded down, never claims more than it is)");
  assert(contextOf(m(5_000_000, 1_000_000))?.percent === 100, "past the window is 100, not 500");
  assert(contextOf(m(0, 1_000_000)) === null && contextOf(m(100, 0)) === null && contextOf(undefined) === null, "both numbers or nothing");
  assert(contextLevel(0) === "ok" && contextLevel(69) === "ok", "ok below 70");
  assert(contextLevel(70) === "warn" && contextLevel(89) === "warn", "warn from 70");
  assert(contextLevel(90) === "danger" && contextLevel(100) === "danger", "danger from 90");
}

// The meter follows the addressed agent, and a later reply that reports nothing
// does not blank it.
function testLatestContext(): void {
  const a = "maya/acme/codex";
  const b = "maya/nimbus/opencode";
  const feed = [
    { agentKey: a, metrics: m(100_000, 1_000_000, "m-a") },
    { agentKey: b, metrics: m(900_000, 1_000_000) },
    { agentKey: a, metrics: m(150_000, 1_000_000, "m-a") },
    { agentKey: a, metrics: undefined }, // a later reply that reports nothing
    { agentKey: undefined, metrics: m(999_999, 1_000_000) }, // a person's message
  ];
  const ua = latestContext(feed, a);
  assert(ua?.tokens === 150_000 && ua.percent === 15 && ua.model === "m-a", `agent a: ${JSON.stringify(ua)}`);
  assert(latestContext(feed, b)?.percent === 90, "agent b is its own conversation");
  assert(latestContext(feed, "jack/acme/codex") === null, "an agent with no reply has no meter");
  assert(latestContext(feed, "") === null, "no addressed agent, no meter");
  assert(latestContext([], a) === null, "an empty feed");
}

testReader();
testPercentAndLevels();
testLatestContext();
