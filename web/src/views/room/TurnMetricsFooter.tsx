import type { TurnMetrics } from "@/lib/api";
import { compactNumber, formatDuration, formatUsd } from "@/lib/format";

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
  const costPart = metrics.reported && metrics.costUsd > 0 ? [formatUsd(metrics.costUsd)] : [];
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
