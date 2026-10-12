import { req } from "./api.ts";
import {
  archiveContentType,
  formPayload,
  type ContentComponent,
  type ContentComponentDetail,
  type ContentForm,
  type ContentPublishRequest,
  type ContentKind,
  type ContentUseScope,
  type ContentUsesReply,
  type ContentVersionDetail,
} from "./contentComponents.ts";

const enc = encodeURIComponent;

type ResubmittedListener = (r: ContentPublishRequest) => void;
const resubmittedListeners = new Set<ResubmittedListener>();

// contentResubmitted tells the app that a save opened a publish request by
// itself (a new version of a component the organization uses): every save
// path goes through contentApi, so one listener tells the author.
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
  const r = d.resubmitted;
  if (r) for (const l of [...resubmittedListeners]) l(r);
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

// Content components (experts, skill packs), served by the gateway
// (services/gateway/content_components.go).
export const contentApi = {
  list: (kind?: ContentKind) =>
    req<{ components: ContentComponent[] }>(`/v1/content-components${kind ? `?kind=${enc(kind)}` : ""}`),
  get: (id: string, roomId?: string) => req<ContentComponentDetail>(`/v1/content-components/${enc(id)}${roomQuery(roomId)}`),
  version: (id: string, version: string, roomId?: string) =>
    req<ContentVersionDetail>(`/v1/content-components/${enc(id)}/versions/${enc(version)}${roomQuery(roomId)}`),
  create: (form: ContentForm) =>
    req<ContentComponentDetail>("/v1/content-components", { method: "POST", body: JSON.stringify({ form: formPayload(form) }) }),
  newVersion: (id: string, form: ContentForm, baseVersion: string) =>
    req<ContentComponentDetail>(`/v1/content-components/${enc(id)}/versions`, {
      method: "POST",
      body: JSON.stringify({ form: formPayload(form), baseVersion }),
    }).then(noteSaved),
  // Import a skill directory, a plugin or a package as a new component, or
  // (existingId) as a new version of one of the caller's.
  importArchive: (file: File, opts: { kind: ContentKind; name?: string; existingId?: string }) => {
    const q = new URLSearchParams();
    if (opts.existingId) q.set("id", opts.existingId);
    else {
      q.set("kind", opts.kind);
      if (opts.name?.trim()) q.set("name", opts.name.trim());
    }
    return postArchive<ContentComponentDetail>(`/v1/content-components/import?${q}`, file).then(noteSaved);
  },
  // A package archive (a tar the web packed from drafted deliverables): a
  // new component (component.json names its kind and name), or a new
  // version of one of the caller's (the server bumps the version).
  createArchive: (tar: Blob) => postBody<ContentComponentDetail>("/v1/content-components", tar, "application/x-tar"),
  newVersionArchive: (id: string, tar: Blob) =>
    postBody<ContentComponentDetail>(`/v1/content-components/${enc(id)}/versions`, tar, "application/x-tar").then(noteSaved),
  remove: (id: string) => req<void>(`/v1/content-components/${enc(id)}`, { method: "DELETE", as: "none" }),
  ack: (id: string, version: string, roomId?: string) =>
    req<void>(`/v1/content-components/${enc(id)}/versions/${enc(version)}/ack${roomQuery(roomId)}`, { method: "POST", as: "none" }),
  uses: (roomId?: string) => req<ContentUsesReply>(`/v1/content-uses${roomQuery(roomId)}`),
  putUse: (scope: ContentUseScope, componentId: string, opts: { roomId?: string; version?: string } = {}) =>
    req<void>("/v1/content-uses", {
      method: "PUT",
      body: JSON.stringify({ scope, componentId, ...(opts.roomId ? { roomId: opts.roomId } : {}), ...(opts.version ? { version: opts.version } : {}) }),
      as: "none",
    }),
  removeUse: (scope: ContentUseScope, componentId: string, roomId?: string) => {
    const q = new URLSearchParams({ scope, componentId });
    if (roomId) q.set("roomId", roomId);
    return req<void>(`/v1/content-uses?${q}`, { method: "DELETE", as: "none" });
  },
  // The bundle archive (download through the artifact entitlement).
  bundle: (bundleUrl: string, roomId?: string): Promise<Blob> => req<Blob>(bundleUrl + roomQuery(roomId), { as: "blob" }),
};

