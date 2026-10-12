import { ApiError, req } from "./api.ts";
import { apiUrl } from "./config.ts";
import { authedFetch, errorDetail } from "./session.ts";
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

const enc = encodeURIComponent;

// roomQuery addresses a component someone else pinned in a room the caller
// is in (read access through the room).
function roomQuery(roomId?: string): string {
  return roomId ? `?room=${enc(roomId)}` : "";
}

// Archive uploads (import, a new version from a package) send the raw file.
async function postArchive<T>(path: string, file: File): Promise<T> {
  const res = await authedFetch(apiUrl(path), {
    method: "POST",
    headers: { "Content-Type": archiveContentType(file.name) },
    body: file,
  });
  if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
  return (await res.json()) as T;
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
    }),
  // Import a skill directory, a plugin or a package as a new component, or
  // (existingId) as a new version of one of the caller's.
  importArchive: (file: File, opts: { kind: ContentKind; name?: string; existingId?: string }) => {
    const q = new URLSearchParams();
    if (opts.existingId) q.set("id", opts.existingId);
    else {
      q.set("kind", opts.kind);
      if (opts.name?.trim()) q.set("name", opts.name.trim());
    }
    return postArchive<ContentComponentDetail>(`/v1/content-components/import?${q}`, file);
  },
  remove: (id: string) => req<void>(`/v1/content-components/${enc(id)}`, { method: "DELETE", raw: true }),
  ack: (id: string, version: string, roomId?: string) =>
    req<void>(`/v1/content-components/${enc(id)}/versions/${enc(version)}/ack${roomQuery(roomId)}`, { method: "POST", raw: true }),
  uses: (roomId?: string) => req<ContentUsesReply>(`/v1/content-uses${roomQuery(roomId)}`),
  putUse: (scope: ContentUseScope, componentId: string, opts: { roomId?: string; version?: string } = {}) =>
    req<void>("/v1/content-uses", {
      method: "PUT",
      body: JSON.stringify({ scope, componentId, ...(opts.roomId ? { roomId: opts.roomId } : {}), ...(opts.version ? { version: opts.version } : {}) }),
      raw: true,
    }),
  removeUse: (scope: ContentUseScope, componentId: string, roomId?: string) => {
    const q = new URLSearchParams({ scope, componentId });
    if (roomId) q.set("roomId", roomId);
    return req<void>(`/v1/content-uses?${q}`, { method: "DELETE", raw: true });
  },
  // The bundle archive (download through the artifact entitlement).
  bundle: async (bundleUrl: string, roomId?: string): Promise<Blob> => {
    const res = await authedFetch(apiUrl(bundleUrl + roomQuery(roomId)));
    if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
    return res.blob();
  },
};

// apiErrorText is the server's own message (the validator explains what is
// wrong) or a fallback.
export function apiErrorText(e: unknown, fallback: string): string {
  return e instanceof ApiError && e.message ? `${fallback}: ${e.message}` : fallback;
}
