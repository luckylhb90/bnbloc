// Room handoff packages (export a room's conversation, artifacts and
// attachments as a zip; import one into a new room). Wire shapes mirror
// internal/wire/handoff.go; the package format is docs/room-export-format.md.
// Everything here is pure so it is unit-tested (handoff.test.ts).

export type { RoomHandoffInfo, RoomImportResult } from "./wire.gen.ts";
import type { RoomHandoffInfo, RoomImportResult } from "./wire.gen.ts";

export type { RoomExportMember as HandoffMember } from "./wire.gen.ts";
import type { RoomExportMember as HandoffMember } from "./wire.gen.ts";

// importedFrom reads the server-stamped payload.imported marker of a history
// row and returns the source room's display name (falling back to its id), or
// undefined for a row that was not imported.
export function importedFrom(payload: Record<string, unknown> | undefined): string | undefined {
  const marker = payload?.imported;
  if (!marker || typeof marker !== "object") return undefined;
  const m = marker as Record<string, unknown>;
  const name = typeof m.fromRoomName === "string" ? m.fromRoomName.trim() : "";
  const id = typeof m.fromRoomId === "string" ? m.fromRoomId.trim() : "";
  return name || id || undefined;
}

// importedEditedAtMs returns the original edit stamp an imported row carries.
export function importedEditedAtMs(payload: Record<string, unknown> | undefined): number | undefined {
  const marker = payload?.imported;
  if (!marker || typeof marker !== "object") return undefined;
  const v = (marker as Record<string, unknown>).editedAtMs;
  return typeof v === "number" ? v : undefined;
}

// handoffBannerText is the imported-room banner line.
export function handoffBannerText(info: RoomHandoffInfo, locale?: string): string {
  const date = new Date(info.importedAtMs).toLocaleDateString(locale, { year: "numeric", month: "short", day: "numeric" });
  const source = info.sourceRoomName || info.sourceRoomId;
  return `Imported from ${source} on ${date}`;
}

// suggestedInviteRole maps a source-room role onto the role an invite
// suggestion adds the member with. Ownership is never handed out by a
// suggestion (only by an explicit transfer), so the source owner and admins
// come back as admins; visitors stay visitors; everyone else is a member.
export function suggestedInviteRole(role: string): "admin" | "member" | "visitor" {
  switch (role) {
    case "owner":
    case "admin":
      return "admin";
    case "visitor":
      return "visitor";
    default:
      return "member";
  }
}

// pendingSuggestions drops suggested members who are already in the room.
export function pendingSuggestions(
  suggested: readonly HandoffMember[],
  currentMembers: readonly string[],
): HandoffMember[] {
  const have = new Set(currentMembers);
  return suggested.filter((m) => !have.has(m.userId));
}

// downloadFilename extracts the filename from a Content-Disposition header,
// falling back to a name derived from the room id.
export function downloadFilename(disposition: string | null, roomId: string): string {
  const m = disposition?.match(/filename\*?=(?:UTF-8'')?"?([^";]+)"?/i);
  if (m) {
    try {
      return decodeURIComponent(m[1]);
    } catch {
      return m[1];
    }
  }
  return `${roomId.replace(/[^\w.-]+/g, "_")}-handoff.zip`;
}

// importSummary is the success toast after an import.
export function importSummary(res: RoomImportResult): string {
  const parts = [`${res.importedMessages} message${res.importedMessages === 1 ? "" : "s"}`];
  if (res.importedArtifacts > 0) parts.push(`${res.importedArtifacts} artifact${res.importedArtifacts === 1 ? "" : "s"}`);
  if (res.importedAttachments > 0) parts.push(`${res.importedAttachments} file${res.importedAttachments === 1 ? "" : "s"}`);
  let msg = `Imported ${parts.join(", ")} into "${res.name}"`;
  if (res.skippedAttachments.length > 0) {
    msg += ` (${res.skippedAttachments.length} attachment${res.skippedAttachments.length === 1 ? "" : "s"} skipped)`;
  }
  return msg;
}
