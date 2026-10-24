import type { TurnMetrics } from "./apiModels.ts";

// ContextUsage is how full an agent's conversation was after its newest reply
// that said so: what the model read for the last request, out of its window.
export interface ContextUsage {
  readonly tokens: number;
  readonly window: number;
  // 0 to 100, never above 100 (a count past the window is "full").
  readonly percent: number;
  readonly model?: string;
}

// How full is "getting full" and "nearly full".
export const CONTEXT_WARN_PERCENT = 70;
export const CONTEXT_DANGER_PERCENT = 90;

export type ContextLevel = "ok" | "warn" | "danger";

export function contextLevel(percent: number): ContextLevel {
  if (percent >= CONTEXT_DANGER_PERCENT) return "danger";
  if (percent >= CONTEXT_WARN_PERCENT) return "warn";
  return "ok";
}

interface Reply {
  readonly agentKey?: string;
  readonly metrics?: TurnMetrics;
}

// contextOf reads one reply's numbers; null unless it reports both (an unknown
// window is never guessed, and an unreported count is not 0%).
export function contextOf(metrics: TurnMetrics | undefined): ContextUsage | null {
  if (!metrics || metrics.contextTokens <= 0 || metrics.contextWindow <= 0) return null;
  const percent = Math.min(100, Math.floor((metrics.contextTokens / metrics.contextWindow) * 100));
  return {
    tokens: metrics.contextTokens,
    window: metrics.contextWindow,
    percent,
    ...(metrics.model ? { model: metrics.model } : {}),
  };
}

// latestContext is the newest reply of the agent (by agentIdentity) that reports
// its context. A later reply of the same agent that reports nothing does not
// hide it: the conversation is still that big. Another agent's replies are
// never read: each agent has its own conversation.
export function latestContext(feed: readonly Reply[], agentKey: string): ContextUsage | null {
  if (!agentKey) return null;
  for (let i = feed.length - 1; i >= 0; i--) {
    const r = feed[i];
    if (r.agentKey !== agentKey) continue;
    const usage = contextOf(r.metrics);
    if (usage) return usage;
  }
  return null;
}
