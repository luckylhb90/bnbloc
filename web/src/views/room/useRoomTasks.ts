import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError, type MessageTask, type TaskStatus } from "@/lib/api";
import type { RoomEvent } from "@/lib/roomEvents";
import { applyTaskUpdate } from "@/lib/team";
import { useToast } from "@/ui/Toast";

export interface RoomTasks {
  readonly tasks: MessageTask[];
  // Task by message sequence (the feed badges rows from it).
  readonly bySeq: ReadonlyMap<number, MessageTask>;
  readonly applyEvent: (e: RoomEvent) => boolean;
  readonly mark: (seq: number) => void;
  readonly setStatus: (seq: number, status: TaskStatus) => void;
  readonly unmark: (seq: number) => void;
}

// useRoomTasks owns a room's message tasks (room-wide, every channel): the
// initial list, live task_updated events from other members, and this
// member's mark / toggle / unmark actions. The server's reply and the event
// carry the same task, so applying both is idempotent.
export function useRoomTasks(roomId: string, joinedGen: number): RoomTasks {
  const toast = useToast();
  const [tasks, setTasks] = useState<MessageTask[]>([]);

  useEffect(() => {
    let cancelled = false;
    api
      .roomTasks(roomId)
      .then((r) => !cancelled && setTasks(r.tasks ?? []))
      .catch(() => {
        // Best-effort: the strip simply stays hidden; live events still apply.
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, joinedGen]);

  const applyEvent = useCallback((e: RoomEvent): boolean => {
    if (e.kind !== "task_updated") return false;
    const update = e.taskUpdate;
    if (update) setTasks((cur) => applyTaskUpdate(cur, update));
    return true;
  }, []);

  const save = useCallback(
    (seq: number, status: TaskStatus, done?: string) => {
      api
        .setTask(roomId, seq, status)
        .then((task) => {
          setTasks((cur) => applyTaskUpdate(cur, { seq, task }));
          if (done) toast.success(done);
        })
        .catch((e) => {
          toast.error(e instanceof ApiError && e.status === 404 ? "That message no longer exists" : "Could not update the task");
        });
    },
    [roomId, toast],
  );
  const setStatus = useCallback((seq: number, status: TaskStatus) => save(seq, status), [save]);
  const mark = useCallback((seq: number) => save(seq, "open", "Marked as task"), [save]);
  const unmark = useCallback(
    (seq: number) => {
      api
        .unmarkTask(roomId, seq)
        .then(() => setTasks((cur) => applyTaskUpdate(cur, { seq, removed: true })))
        .catch(() => toast.error("Could not remove the task"));
    },
    [roomId, toast],
  );

  const bySeq = useMemo(() => new Map(tasks.map((t) => [t.seq, t])), [tasks]);
  return { tasks, bySeq, applyEvent, mark, setStatus, unmark };
}
