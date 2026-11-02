import { useCallback, useEffect, useState } from "react";
import { channelsApi } from "@/lib/channelsApi";
import { errorText } from "@/lib/errors";
import { applyMark } from "@/lib/messageMarks";
import type { RoomEvent } from "@/lib/roomEvents";
import type { MessageMark, MessageMarkKind } from "@/lib/wire.gen";
import { useToast } from "@/ui/Toast";

export interface RoomMarks {
  readonly marks: readonly MessageMark[];
  readonly applyEvent: (e: RoomEvent) => boolean;
  // Puts a mark on a message (on: true) or takes it off; a reaction names
  // its emoji.
  readonly setMark: (seq: number, kind: MessageMarkKind, on: boolean, value?: string) => void;
}

const failed: Record<MessageMarkKind, string> = {
  reaction: "Could not change the reaction",
  pin: "Could not change the pin",
  saved: "Could not change the saved message",
};

// useRoomMarks owns a room's reactions, pins and the member's saved messages
// (channels PRD §2.3): the listing, live message_marked events from others,
// and this member's changes. The server's reply and the event carry the
// same mark, so applying both is idempotent.
export function useRoomMarks(roomId: string, joinedGen: number, subject: string | null): RoomMarks {
  const toast = useToast();
  const [marks, setMarks] = useState<MessageMark[]>([]);

  useEffect(() => {
    let cancelled = false;
    channelsApi
      .marks(roomId)
      .then((r) => !cancelled && setMarks(r.marks ?? []))
      .catch(() => {
        // Best-effort: rows show no marks until one arrives live.
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, joinedGen]);

  const applyEvent = useCallback((e: RoomEvent): boolean => {
    if (e.kind !== "message_marked") return false;
    const update = e.markUpdate;
    if (update) setMarks((cur) => applyMark(cur, update));
    return true;
  }, []);

  const setMark = useCallback(
    (seq: number, kind: MessageMarkKind, on: boolean, value?: string) => {
      const body = { kind, ...(value ? { value } : {}) };
      const mark: MessageMark = {
        seq,
        kind,
        ...(kind !== "pin" && subject ? { userId: subject } : {}),
        ...(value ? { value } : {}),
        by: subject ?? "",
        createdAtMs: Date.now(),
      };
      const call = on ? channelsApi.putMark(roomId, seq, body) : channelsApi.removeMark(roomId, seq, body).then(() => mark);
      call
        .then((m) => setMarks((cur) => applyMark(cur, { mark: m, removed: !on })))
        .catch((e) => toast.error(errorText(e, failed[kind])));
    },
    [roomId, subject, toast],
  );

  return { marks, applyEvent, setMark };
}
