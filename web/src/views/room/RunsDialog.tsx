import { useCallback } from "react";
import type { AgentRun, RunListQuery } from "@/lib/agentRuns";
import { agentRunsApi } from "@/lib/agentRunsApi";
import { messageJumps } from "@/lib/team";
import { Dialog } from "@/ui/Dialog";
import { RunsBrowser } from "@/views/runs/RunsBrowser";

// RunsDialog is the room's agent run records panel (R10): every run any agent
// did in this room, with timing, outcome, reply preview and usage. Jumping to
// a run's request or reply closes the panel and scrolls the feed to it.
export function RunsDialog({ roomId, onClose }: { readonly roomId: string; readonly onClose: () => void }) {
  const load = useCallback((q: RunListQuery) => agentRunsApi.room(roomId, q), [roomId]);
  const jump = (run: AgentRun, seq: number) => {
    messageJumps.queue(run.roomId, { seq, channelId: run.channelId || undefined });
    onClose();
  };
  return (
    <Dialog open onClose={onClose} title="Agent runs" description="Every agent run in this room, newest first." size="lg">
      <RunsBrowser
        load={load}
        showRoom={false}
        onJump={jump}
        emptyHint="Runs appear here once someone asks an agent in this room."
      />
    </Dialog>
  );
}
