// What this browser remembers for a member in a room
// (docs/prd/2026-10-28-channels-and-threads.md §2.1b, §2.5a): the rail's
// folded sections (personal order: it changes nothing for others) and
// whether the room's welcome page was shown. Per member on this gateway
// (memberKey), never shared between members.
import { gatewayBase } from "./config.ts";
import { StorageKey, memberKey, readStored, writeStored } from "./storageKeys.ts";

const key = (base: typeof StorageKey.foldedSections | typeof StorageKey.roomWelcomeSeen, subject: string, roomId: string) =>
  memberKey(base, { gateway: gatewayBase(), subject }, roomId);

/** foldedSections is the section ids the member folded in the room. */
export function foldedSections(subject: string | null, roomId: string): string[] {
  if (!subject) return [];
  try {
    const v: unknown = JSON.parse(readStored(key(StorageKey.foldedSections, subject, roomId)) || "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
  } catch {
    return [];
  }
}

export function setFoldedSections(subject: string | null, roomId: string, ids: readonly string[]): void {
  if (!subject) return;
  writeStored(key(StorageKey.foldedSections, subject, roomId), ids.length > 0 ? JSON.stringify(ids) : "");
}

/** welcomeSeen: the member saw the room's welcome page in this browser. */
export function welcomeSeen(subject: string | null, roomId: string): boolean {
  return !!subject && readStored(key(StorageKey.roomWelcomeSeen, subject, roomId)) === "1";
}

export function markWelcomeSeen(subject: string | null, roomId: string): void {
  if (subject) writeStored(key(StorageKey.roomWelcomeSeen, subject, roomId), "1");
}
