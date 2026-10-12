// Where a turn's deliverable cards render. A version's sourceSeq is the
// message that STARTED the turn (the request), but the files are the answer:
// they belong under the agent's reply, not above it. placeDeliverables moves
// each request's cards to the last reply of its turn:
//
// - with the turn's agent known (its job's owner/provider), the last reply
//   row of that agent after the request, before the next request that the
//   same agent was asked;
// - otherwise, the last of the agent replies right after the request.
//
// Until a reply arrives (a live turn), the cards stay under the request.

import { agentHandle } from "./roomAgents.ts";

export interface PlacementRow {
  readonly seq?: number;
  readonly author: string;
  readonly agentLabel?: string;
}

export interface PlacementJob {
  readonly ownerUserId: string;
  readonly core: string;
  readonly provider: string;
}

/** Cards to render after a row: the request seq they came from and their ids. */
export interface PlacedDeliverables {
  readonly seq: number;
  readonly artifactIds: readonly string[];
}

const isAgentReply = (row: PlacementRow) => row.author === "agent";
const rowSeq = (row: PlacementRow) => row.seq ?? 0;
const jobHandle = (job: PlacementJob | undefined) =>
  job && job.provider ? agentHandle({ ownerId: job.ownerUserId, core: job.core || undefined, provider: job.provider }) : "";

/** Row index → the deliverables rendered after that row. */
export function placeDeliverables(
  feed: readonly PlacementRow[],
  bySeq: Readonly<Record<number, readonly string[]>>,
  jobsBySeq: Readonly<Record<number, readonly PlacementJob[]>>,
): Map<number, PlacedDeliverables[]> {
  const out = new Map<number, PlacedDeliverables[]>();
  feed.forEach((row, i) => {
    if (isAgentReply(row)) return;
    const seq = rowSeq(row);
    const ids = seq > 0 ? bySeq[seq] : undefined;
    if (!ids || ids.length === 0) return;
    const handle = jobHandle(jobsBySeq[seq]?.[0]);
    let at = i;
    for (let j = i + 1; j < feed.length; j++) {
      const next = feed[j];
      if (!isAgentReply(next)) {
        if (!handle) break;
        // The same agent's next request starts its next turn.
        if (jobHandle(jobsBySeq[rowSeq(next)]?.[0]) === handle) break;
        continue;
      }
      if (!handle || next.agentLabel === handle) at = j;
    }
    out.set(at, [...(out.get(at) ?? []), { seq, artifactIds: ids }]);
  });
  return out;
}
