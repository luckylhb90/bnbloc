import { req } from "./api.ts";
import { changing } from "./contentChanges.ts";
import {
  archiveContentType,
  formPayload,
  type ContentComponent,
  type ContentComponentDetail,
  type ContentForm,
  type ContentKind,
  type ContentUseScope,
  type ContentUsesReply,
  type ContentVersionDetail,
} from "./contentComponents.ts";
import type { RoomInclude } from "./wire.gen.ts";

const enc = encodeURIComponent;

type ResubmittedListener = (d: Pick<ContentComponentDetail, "resubmitted" | "orgPublished">) => void;
const resubmittedListeners = new Set<ResubmittedListener>();

// contentResubmitted tells the app that a save of a new version of a
// component the organization uses went to the organization by itself: a
// publish request, or published at once where no approval is required.
// Every save path goes through contentApi, so one listener tells the
// author.
export const contentResubmitted = {
  subscribe(l: ResubmittedListener): () => void {
    resubmittedListeners.add(l);
    return () => {
      resubmittedListeners.delete(l);
    };
  },
};

// noteSaved passes a save's detail through, announcing its resubmission.
export function noteSaved(d: ContentComponentDetail): ContentComponentDetail {
  if (d.resubmitted || d.orgPublished) for (const l of [...resubmittedListeners]) l(d);
  return d;
}

// roomQuery addresses a component someone else pinned in a room the caller
// is in (read access through the room).
function roomQuery(roomId?: string): string {
  return roomId ? `?room=${enc(roomId)}` : "";
}

// Archive uploads (import, a new version from a package) send the raw file.
async function postArchive<T>(path: string, file: File): Promise<T> {
  return postBody<T>(path, file, archiveContentType(file.name));
}

async function postBody<T>(path: string, body: Blob, contentType: string): Promise<T> {
  return req<T>(path, { method: "POST", headers: { "Content-Type": contentType }, body });
}

// kindOf reads a save's kind from its detail (lib/contentChanges.ts).
const kindOf = (d: ContentComponentDetail) => d.component.kind;

// Content components (experts, skill packs), served by the gateway
// (services/gateway/content_components.go). Every mutation announces itself
// (changing), so every list that shows content reads it again.
export const contentApi = {
  list: (kind?: ContentKind) =>
    req<{ components: ContentComponent[] }>(`/v1/content-components${kind ? `?kind=${enc(kind)}` : ""}`),
  get: (id: string, roomId?: string) => req<ContentComponentDetail>(`/v1/content-components/${enc(id)}${roomQuery(roomId)}`),
  version: (id: string, version: string, roomId?: string) =>
    req<ContentVersionDetail>(`/v1/content-components/${enc(id)}/versions/${enc(version)}${roomQuery(roomId)}`),
  // A room component's save may include apps made in its room and room
  // files (include: the gateway reads them from the room).
  create: (form: ContentForm, include?: RoomInclude | null) =>
    changing(
      req<ContentComponentDetail>("/v1/content-components", { method: "POST", body: JSON.stringify({ form: formPayload(form), ...(include ? { include } : {}) }) }),
      kindOf,
    ),
  newVersion: (id: string, form: ContentForm, baseVersion: string, include?: RoomInclude | null) =>
    changing(
      req<ContentComponentDetail>(`/v1/content-components/${enc(id)}/versions`, {
        method: "POST",
        body: JSON.stringify({ form: formPayload(form), baseVersion, ...(include ? { include } : {}) }),
      }).then(noteSaved),
      kindOf,
    ),
  // Import a skill directory, a plugin or a package as a new component, or
  // (existingId) as a new version of one of the caller's; a display name,
  // description and tags given replace what the import reads from the files.
  importArchive: (
    file: File,
    opts: { kind: ContentKind; name?: string; existingId?: string; displayName?: string; description?: string; tags?: readonly string[] },
  ) => {
    const q = new URLSearchParams();
    if (opts.existingId) q.set("id", opts.existingId);
    else {
      q.set("kind", opts.kind);
      if (opts.name?.trim()) q.set("name", opts.name.trim());
    }
    if (opts.displayName?.trim()) q.set("displayName", opts.displayName.trim());
    if (opts.description?.trim()) q.set("description", opts.description.trim());
    for (const t of opts.tags ?? []) q.append("tag", t);
    return changing(postArchive<ContentComponentDetail>(`/v1/content-components/import?${q}`, file).then(noteSaved), kindOf);
  },
  // A package archive (a tar the web packed from drafted deliverables): a
  // new component (component.json names its kind and name), or a new
  // version of one of the caller's (the server bumps the version).
  createArchive: (tar: Blob) => changing(postBody<ContentComponentDetail>("/v1/content-components", tar, "application/x-tar"), kindOf),
  newVersionArchive: (id: string, tar: Blob) =>
    changing(postBody<ContentComponentDetail>(`/v1/content-components/${enc(id)}/versions`, tar, "application/x-tar").then(noteSaved), kindOf),
  remove: (id: string) => changing(req<void>(`/v1/content-components/${enc(id)}`, { method: "DELETE", as: "none" }), null),
  ack: (id: string, version: string, roomId?: string) =>
    req<void>(`/v1/content-components/${enc(id)}/versions/${enc(version)}/ack${roomQuery(roomId)}`, { method: "POST", as: "none" }),
  uses: (roomId?: string) => req<ContentUsesReply>(`/v1/content-uses${roomQuery(roomId)}`),
  // A use changes what a list shows as on and what a composer offers.
  putUse: (scope: ContentUseScope, componentId: string, opts: { roomId?: string; version?: string } = {}) =>
    changing(
      req<void>("/v1/content-uses", {
        method: "PUT",
        body: JSON.stringify({ scope, componentId, ...(opts.roomId ? { roomId: opts.roomId } : {}), ...(opts.version ? { version: opts.version } : {}) }),
        as: "none",
      }),
      null,
    ),
  removeUse: (scope: ContentUseScope, componentId: string, roomId?: string) => {
    const q = new URLSearchParams({ scope, componentId });
    if (roomId) q.set("roomId", roomId);
    return changing(req<void>(`/v1/content-uses?${q}`, { method: "DELETE", as: "none" }), null);
  },
  // The bundle archive (download through the artifact entitlement).
  bundle: (bundleUrl: string, roomId?: string): Promise<Blob> => req<Blob>(bundleUrl + roomQuery(roomId), { as: "blob" }),
};

