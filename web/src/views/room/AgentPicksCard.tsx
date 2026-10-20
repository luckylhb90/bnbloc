import { useEffect, useState } from "react";
import { RobotIcon } from "@phosphor-icons/react";
import { agentToPick, type RoomSchedule } from "@/lib/schedules";
import { schedulesApi } from "@/lib/schedulesApi";
import { Button } from "@/ui/Button";

// AgentPicksCard is what the room still needs from the member: their
// schedules that ask an agent but name none yet (a room component's, after
// its install: docs/prd/2026-10-20-complete-room-capture.md §2.2). Each
// opens that schedule's editor in the Schedules dialog, whose agent picker
// sets it; the member then turns it on there. It is gone once each has an
// agent. epoch reloads it (the dialog closed).
export function AgentPicksCard({
  roomId,
  subject,
  epoch,
  onPick,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly epoch: number;
  readonly onPick: (scheduleId: string) => void;
}) {
  const [list, setList] = useState<RoomSchedule[]>([]);
  useEffect(() => {
    let live = true;
    schedulesApi
      .list(roomId)
      .then((r) => live && setList(r.schedules ?? []))
      .catch(() => live && setList([]));
    return () => {
      live = false;
    };
  }, [roomId, epoch]);
  const picks = agentToPick(list, subject);
  if (picks.length === 0) return null;
  return (
    <div role="status" data-agent-picks className="flex flex-col gap-1.5 border-b border-[var(--color-border)] bg-[var(--color-surface-2)] px-4 py-3 text-sm">
      <div className="flex items-start gap-2">
        <RobotIcon size={18} className="mt-0.5 shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <p className="min-w-0 flex-1 text-[var(--color-fg-muted)]">
          {picks.length === 1 ? "A schedule of yours asks an agent" : `${picks.length} schedules of yours ask an agent`}: pick one of your agents, then turn it on.
        </p>
      </div>
      <ul className="flex flex-col gap-1 pl-7">
        {picks.map((s) => (
          <li key={s.scheduleId} className="flex items-center gap-2">
            <span className="min-w-0 flex-1 truncate" title={s.text}>
              Pick an agent for: {s.text}
            </span>
            <Button size="sm" variant="outline" onClick={() => onPick(s.scheduleId)}>
              Pick an agent
            </Button>
          </li>
        ))}
      </ul>
    </div>
  );
}
