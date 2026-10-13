import { req } from "./api.ts";
import { roomPath } from "./roomId.ts";
import type { AppDataStats, AppDataWiped, AppExportFormat } from "./wire.gen.ts";

// An interactive app's data as its owners reach it from the room: the
// gateway's room entry (/v1/rooms/{roomId}/apps/{artifactId}/…, the
// session's own room reach; services/gateway/apps.go). The stats, the
// export and the delete are owners-only.

function appPath(roomId: string, artifactId: string, ...rest: string[]): string {
  return roomPath(roomId, "apps", artifactId, ...rest);
}

export const appDataApi = {
  // The app's document counts (owners; 403 otherwise).
  stats: (roomId: string, artifactId: string) => req<AppDataStats>(appPath(roomId, artifactId, "stats")),
  // The app's data as a file, streamed and audited by the server: JSON of
  // every collection (no collection) or of one, CSV of one (owners).
  export: (roomId: string, artifactId: string, format: AppExportFormat, collection?: string): Promise<Blob> => {
    const q = new URLSearchParams({ format });
    if (collection) q.set("collection", collection);
    return req<Blob>(`${appPath(roomId, artifactId, "export")}?${q}`, { as: "blob" });
  },
  // Deletes all of the app's data, for everyone (owners; irreversible).
  wipe: (roomId: string, artifactId: string) => req<AppDataWiped>(appPath(roomId, artifactId, "data"), { method: "DELETE" }),
};
