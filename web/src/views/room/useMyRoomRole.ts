import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { sameUser } from "@/lib/format";

// useMyRoomRole resolves the signed-in member's role in a room from the member
// list (the room detail does not carry it). null while loading or when the
// caller is not a member. gen re-reads it (e.g. after joining).
export function useMyRoomRole(roomId: string, subject: string | null, gen = 0): string | null {
  const [role, setRole] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    if (!subject) {
      setRole(null);
      return;
    }
    api
      .members(roomId)
      .then((m) => live && setRole((m.members || []).find((x) => sameUser(x.userId, subject))?.role ?? null))
      .catch(() => live && setRole(null));
    return () => {
      live = false;
    };
  }, [roomId, subject, gen]);
  return role;
}
