import { req } from "./api.ts";
import { changing } from "./contentChanges.ts";
import type { ContentComponent, ContentComponentDetail } from "./contentComponents.ts";
import { noteSaved } from "./contentComponentsApi.ts";
import { roomPath } from "./roomId.ts";
import type { AppDemo, AppUseReply, MakeSimilarDraft, WorkPublishRequest, WorkRecipeCapture } from "./workGallery.ts";
import type { AppUseRequest, MakeSimilarRequest, WorkCoverReply, WorkCoverRequest, WorkCoverStored } from "./wire.gen.ts";

const enc = encodeURIComponent;

// The works gallery's requests (services/gateway/work_gallery.go,
// work_recipe.go, make_similar.go, app_demo.go). The gallery's works are
// the content listing's (contentApi.list("work")); a work's cover is its
// listing's coverUrl, read with the session as an image (404 when it has
// none).
export const workGalleryApi = {
  cover: (coverUrl: string) => req<Blob>(coverUrl, { as: "blob" }),
  // A work version's one file (a work that is not a site), or with path
  // one of its recipe's reference files: what the detail's viewers render.
  file: (c: Pick<ContentComponent, "id" | "version">, path?: string) =>
    req<Blob>(`/v1/content-components/${enc(c.id)}/versions/${enc(c.version)}/file${path ? `?path=${enc(path)}` : ""}`, { as: "blob" }),
  // A work version's stored cover (work covers PRD §2.3): a reader's
  // picture of it (a PNG, WebP or JPEG; kept only while it has none), and
  // the author's AI cover (drawn by the gateway, which replaces a render).
  putCover: (c: Pick<ContentComponent, "id" | "version">, image: Blob) =>
    changing(
      req<WorkCoverStored>(`/v1/content-components/${enc(c.id)}/versions/${enc(c.version)}/cover?source=render`, {
        method: "PUT",
        headers: { "Content-Type": image.type },
        body: image,
      }),
      "work",
    ),
  drawCover: (c: Pick<ContentComponent, "id" | "version">) =>
    changing(req<WorkCoverStored>(`/v1/content-components/${enc(c.id)}/versions/${enc(c.version)}/cover/ai`, { method: "POST" }), "work"),
  favorite: (id: string, on: boolean) =>
    changing(req<void>(`/v1/content-components/${enc(id)}/favorite`, { method: on ? "PUT" : "DELETE", as: "none" }), "work"),
  // A site or app work's site in the sandboxed frame: a site URL whose SDK
  // keeps its data in the page (an app's Try demo, a site's preview).
  demo: (c: Pick<ContentComponent, "id" | "version">) =>
    req<AppDemo>(`/v1/content-components/${enc(c.id)}/versions/${enc(c.version)}/demo`, { method: "POST" }),
  // A copy of an app as a new site in a room the caller owns, or a new room.
  use: (id: string, body: AppUseRequest) =>
    req<AppUseReply>(`/v1/content-components/${enc(id)}/use-app`, { method: "POST", body: JSON.stringify(body) }),
  // A draft of the work's recipe in a room the caller owns, or a new room.
  makeSimilar: (id: string, body: MakeSimilarRequest) =>
    req<MakeSimilarDraft>(`/v1/content-components/${enc(id)}/make-similar`, { method: "POST", body: JSON.stringify(body) }),
  // What publishing a deliverable's current version would capture.
  recipe: (roomId: string, artifactId: string) => req<WorkRecipeCapture>(roomPath(roomId, "artifacts", artifactId, "recipe")),
  // An AI cover for a deliverable about to be published (work covers PRD
  // §2.2): drawn only when the person asks, kept only if they publish it.
  makeCover: (roomId: string, artifactId: string, body: WorkCoverRequest) =>
    req<WorkCoverReply>(roomPath(roomId, "artifacts", artifactId, "work-cover"), { method: "POST", body: JSON.stringify(body) }),
  // A deliverable's current version as a work of the caller's (a new
  // version of a work the organization uses announces where it went).
  publish: (roomId: string, artifactId: string, body: WorkPublishRequest) =>
    changing(req<ContentComponentDetail>(roomPath(roomId, "artifacts", artifactId, "publish-work"), { method: "POST", body: JSON.stringify(body) }).then(noteSaved), "work"),
};
