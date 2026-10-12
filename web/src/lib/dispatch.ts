// Agent dispatch attribution (room tools). A chat_message an agent dispatched
// on a person's behalf carries payload.dispatchedBy {jobId, depth, via}
// (internal/wire/dispatch.go): the parent job, the hop depth and the
// dispatching agent's handle. The expert the target was asked to wear is the
// message's ordinary payload.expert.
// The message's author is the person whose request it serves; the feed shows
// the chain as "maya → via jack/codex → bob/claude".

export interface DispatchAttribution {
  readonly jobId: string;
  readonly depth: number;
  // The dispatching agent's handle ("jack/codex").
  readonly via: string;
  // The addressed agent's handle ("bob/claude"), from payload.recipient.
  readonly to?: string;
  readonly expert?: string;
}

// dispatchAttribution reads payload.dispatchedBy (and the recipient) from a
// chat payload; undefined for a message nobody dispatched.
export function dispatchAttribution(payload: unknown): DispatchAttribution | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const d = (payload as { dispatchedBy?: unknown }).dispatchedBy;
  if (!d || typeof d !== "object") return undefined;
  const { jobId, depth, via } = d as { jobId?: unknown; depth?: unknown; via?: unknown };
  const e = (payload as { expert?: { name?: unknown; displayName?: unknown } }).expert;
  const expert = typeof e?.displayName === "string" && e.displayName ? e.displayName : typeof e?.name === "string" && e.name ? e.name : undefined;
  if (typeof jobId !== "string" || !jobId) return undefined;
  const recipient = (payload as { recipient?: { ownerId?: unknown; provider?: unknown } }).recipient;
  const to =
    recipient && typeof recipient.provider === "string" && recipient.provider
      ? typeof recipient.ownerId === "string" && recipient.ownerId
        ? `${recipient.ownerId}/${recipient.provider}`
        : recipient.provider
      : undefined;
  return {
    jobId,
    depth: typeof depth === "number" && depth > 0 ? depth : 1,
    via: typeof via === "string" && via ? via : "an agent",
    to,
    expert,
  };
}

// dispatchLine is the plain-text chain for a title or a screen reader:
// "maya → via jack/codex → bob/claude (as reviewer)".
export function dispatchLine(author: string, d: DispatchAttribution): string {
  const target = d.to ? ` → ${d.to}` : "";
  const role = d.expert ? ` (as ${d.expert})` : "";
  return `${author} → via ${d.via}${target}${role}`;
}
