// Opening a local document in bnbloc (docs/prd/2026-10-14-ai-document-editing.md
// §4 rules 2–4, M3). The member's copy becomes a room file: a document
// opened before goes back to the room file it became (a changed copy is
// its next version); any other lands in the member's private "My files"
// room. The path stays in the shell (lib/localFiles.ts); the gateway sees
// the file name and bytes only. The steps are injected (OpenDeps) so the
// rules are unit-tested under node.

import type { LocalBinding, LocalOpened } from "./localFiles.ts";

export const MY_FILES_NAME = "My files";
export const MY_FILES_DESCRIPTION = "Documents you opened from this computer. Only you are in this room until you invite someone.";

export interface OpenDeps {
  readonly gateway: string;
  /** The member's "My files" room (created when absent). */
  ensureRoom(): Promise<string>;
  read(token: string): Promise<ArrayBuffer>;
  bind(token: string, binding: LocalBinding): Promise<void>;
  /** The room file's current version; null when it is gone. */
  currentVersion(roomId: string, artifactId: string): Promise<number | null>;
  /** A new, empty room file named name; its id. */
  createFile(roomId: string, name: string): Promise<string>;
  /** Publishes bytes as the version after baseVersion; the version it got. */
  publish(roomId: string, artifactId: string, bytes: ArrayBuffer, name: string, baseVersion: number, summary: string): Promise<number>;
}

export interface OpenedFile {
  readonly roomId: string;
  readonly artifactId: string;
  readonly version: number;
}

export async function openLocal(o: LocalOpened, d: OpenDeps): Promise<OpenedFile> {
  const bound = o.binding?.gateway === d.gateway ? o.binding : null;
  if (bound) {
    const current = await d.currentVersion(bound.roomId, bound.artifactId);
    if (current !== null) {
      const at = { roomId: bound.roomId, artifactId: bound.artifactId };
      if (!o.changed) return { ...at, version: current };
      const bytes = await d.read(o.token);
      const version = await d.publish(at.roomId, at.artifactId, bytes, o.name, current, "Changed on this computer");
      await d.bind(o.token, { ...bound, version });
      return { ...at, version };
    }
  }
  const roomId = await d.ensureRoom();
  const bytes = await d.read(o.token);
  const artifactId = await d.createFile(roomId, o.name);
  const version = await d.publish(roomId, artifactId, bytes, o.name, 0, "Opened from this computer");
  await d.bind(o.token, { gateway: d.gateway, roomId, artifactId, version });
  return { roomId, artifactId, version };
}

/** The member's "My files" room id: one per member and organization, the
 *  same on every machine (room ids are global, so it is a hash). */
export async function myFilesRoomId(tenant: string, subject: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(`${tenant}\n${subject}`));
  const hex = [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
  return `my-files-${hex.slice(0, 16)}`;
}
