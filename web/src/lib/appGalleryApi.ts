import { req } from "./api.ts";
import type { ContentComponent, ContentComponentDetail } from "./contentComponents.ts";
import { noteSaved } from "./contentComponentsApi.ts";
import { roomPath } from "./roomId.ts";
import type { AppDemo, WorkPublishRequest, AppUseReply, AppUseRequest } from "./appGallery.ts";

const enc = encodeURIComponent;

// The app gallery's requests (services/gateway/app_gallery.go,
// app_demo.go). The gallery's apps are the content listing's
// (contentApi.list("app")); an app's cover is its listing's coverUrl,
// read with the session as an image (404 when the app ships none).
export const appGalleryApi = {
  cover: (coverUrl: string) => req<Blob>(coverUrl, { as: "blob" }),
  favorite: (id: string, on: boolean) =>
    req<void>(`/v1/content-components/${enc(id)}/favorite`, { method: on ? "PUT" : "DELETE", as: "none" }),
  // A demo of one version: a site URL whose SDK keeps its data in the page.
  demo: (c: Pick<ContentComponent, "id" | "version">) =>
    req<AppDemo>(`/v1/content-components/${enc(c.id)}/versions/${enc(c.version)}/demo`, { method: "POST" }),
  // A copy of the app as a new site in a room the caller owns, or a new room.
  use: (id: string, body: AppUseRequest) =>
    req<AppUseReply>(`/v1/content-components/${enc(id)}/use-app`, { method: "POST", body: JSON.stringify(body) }),
  // A site deliverable's current version as an app of the caller's (a new
  // version of an app the organization uses announces where it went).
  publish: (roomId: string, artifactId: string, body: WorkPublishRequest) =>
    req<ContentComponentDetail>(roomPath(roomId, "artifacts", artifactId, "publish-work"), { method: "POST", body: JSON.stringify(body) }).then(noteSaved),
};
