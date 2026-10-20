import { useCallback, useState } from "react";
import type { AgentRun, RunListQuery, RunScope } from "@/lib/agentRuns";
import { agentRunsApi } from "@/lib/agentRunsApi";
import { messageJumps } from "@/lib/team";
import { Segment } from "@/ui/Segment";
import { RunsBrowser, type RunAgentFilter } from "@/views/runs/RunsBrowser";
import { RoomToolUse } from "@/views/team/RoomToolUse";

const SCOPES = [
  { value: "tenant" as const, label: "Team" },
  { value: "self" as const, label: "Mine" },
];

// TeamRunsTab lists agent run records across rooms (R10): a member's own
// (runs of their agents and runs they requested), or for org admins the whole
// organization. Text of rooms the viewer is not in stays hidden server-side.
export function TeamRunsTab({
  canSeeTeam,
  agent,
  onClearAgent,
  onOpenRoom,
}: {
  readonly canSeeTeam: boolean;
  readonly agent: RunAgentFilter | null;
  readonly onClearAgent: () => void;
  readonly onOpenRoom: (id: string) => void;
}) {
  const [scope, setScope] = useState<Exclude<RunScope, "room">>(canSeeTeam ? "tenant" : "self");
  const effective = canSeeTeam ? scope : "self";
  const load = useCallback(
    (q: RunListQuery) => (effective === "tenant" ? agentRunsApi.tenant(q) : agentRunsApi.mine(q)),
    [effective],
  );
  const jump = (run: AgentRun, seq: number) => {
    messageJumps.queue(run.roomId, { seq, channelId: run.channelId || undefined });
    onOpenRoom(run.roomId);
  };
  return (
    <div className="flex flex-col gap-3">
      {canSeeTeam && <Segment options={SCOPES} value={scope} onChange={setScope} className="w-40" />}
      {effective === "tenant" && <RoomToolUse />}
      <RunsBrowser
        key={effective}
        load={load}
        showRoom
        memberFilter={effective === "tenant"}
        agent={agent}
        onClearAgent={onClearAgent}
        onJump={jump}
        emptyHint={
          effective === "tenant"
            ? "Runs appear here once members ask agents in their rooms."
            : "Runs of your agents, and runs you asked for, appear here."
        }
      />
    </div>
  );
}
