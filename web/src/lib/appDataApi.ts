import { req } from "./api.ts";
import { allDocs, type AppDoc } from "./appData.ts";
import { roomPath } from "./roomId.ts";
import { MaxAppFindLimit, type AppDataStats, type AppDataWiped, type AppDocPage } from "./wire.gen.ts";

// An interactive app's data as its owners reach it from the room: the
// gateway's room entry (/v1/rooms/{roomId}/apps/{artifactId}/…, the
// session's own room reach; services/gateway/apps.go). Owners read every
// document of the app there; the stats and the delete are owners-only.

function appPath(roomId: string, artifactId: string, ...rest: string[]): string {
  return roomPath(roomId, "apps", artifactId, ...rest);
}

export const appDataApi = {
  // The app's document counts (owners; 403 otherwise).
  stats: (roomId: string, artifactId: string) => req<AppDataStats>(appPath(roomId, artifactId, "stats")),
  // Every document of one collection, page by page.
  collection: (roomId: string, artifactId: string, collection: string): Promise<AppDoc[]> =>
    allDocs((after) => {
      const q = new URLSearchParams({ limit: String(MaxAppFindLimit) });
      if (after) q.set("after", after);
      return req<AppDocPage>(`${appPath(roomId, artifactId, "c", collection)}?${q}`);
    }),
  // Deletes all of the app's data, for everyone (owners; irreversible).
  wipe: (roomId: string, artifactId: string) => req<AppDataWiped>(appPath(roomId, artifactId, "data"), { method: "DELETE" }),
};
