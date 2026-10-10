import type { TurnMetrics } from "@/lib/api";
import { compactNumber } from "@/lib/format";

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 10_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.round(ms / 1000)}s`;
}

function formatCost(costUsd: number): string {
  if (costUsd < 0.001) return "<$0.001";
  return `$${costUsd.toFixed(3)}`;
}

function metricTitle(metrics: TurnMetrics): string {
  const parts = [
    metrics.model ? `model ${metrics.model}` : "model unknown",
    metrics.reported
      ? `cache read ${compactNumber(metrics.cacheReadTokens)}, cache created ${compactNumber(metrics.cacheCreationTokens)}, thinking ${compactNumber(metrics.thinkingTokens)}`
      : "tokens and cost not reported by model",
  ];
  if (metrics.ttftMs > 0) parts.push(`TTFT ${formatDuration(metrics.ttftMs)}`);
  if (metrics.durationApiMs > 0) parts.push(`API duration ${formatDuration(metrics.durationApiMs)}`);
  return parts.join(" · ");
}

export function TurnMetricsFooter({ metrics }: { readonly metrics: TurnMetrics }) {
  const cacheTokens = metrics.cacheCreationTokens + metrics.cacheReadTokens;
  const reportedParts = metrics.reported
    ? [
        `${compactNumber(metrics.inputTokens)} in`,
        `${compactNumber(metrics.outputTokens)} out`,
        `${compactNumber(cacheTokens)} cache`,
      ]
    : ["tokens not reported"];
  const timingParts = [
    metrics.ttftMs > 0 ? `TTFT ${formatDuration(metrics.ttftMs)}` : "",
    metrics.durationApiMs > 0 ? formatDuration(metrics.durationApiMs) : "",
  ].filter(Boolean);
  const costPart = metrics.reported && metrics.costUsd > 0 ? [formatCost(metrics.costUsd)] : [];
  return (
    <div
      title={metricTitle(metrics)}
      className="mt-2 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[0.68rem] text-[var(--color-fg-subtle)] tabular"
    >
      {[...reportedParts, ...timingParts, ...costPart].map((part, index) => (
        <span key={part} className="inline-flex items-center gap-1">
          {index > 0 && <span className="text-[var(--color-border-strong)]">·</span>}
          <span>{part}</span>
        </span>
      ))}
    </div>
  );
}
