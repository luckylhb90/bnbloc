import { req } from "./api.ts";
import { roomPath } from "./roomId.ts";
import { myAppsQuery, type MyAppList } from "./myApps.ts";

// A member's apps and an app's cover. The listing is the room service's
// (services/room/my_apps.go); a cover is one site version's cover image,
// served by the gateway as an image only (services/gateway/site_cover.go):
// 404 when the site has none, 413/415 when it is refused.

export const myAppsApi = {
  // One page of the caller's apps, newest-updated first.
  list: (cursor?: string, limit?: number) => req<MyAppList>(`/v1/my/apps${myAppsQuery(cursor, limit)}`),
  // A site version's cover image.
  cover: (roomId: string, artifactId: string, version: number) =>
    req<Blob>(roomPath(roomId, "artifacts", artifactId, "versions", version, "cover"), { as: "blob" }),
};
