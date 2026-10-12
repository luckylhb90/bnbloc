import { uploadArtifact as uploadBinaryArtifact, artifactUrl, type ArtifactUploadOptions } from "./artifactUpload.ts";
import { downloadReleaseFile as fetchReleaseFile } from "./releaseDownload.ts";
import { saveBlob } from "./saveBlob.ts";
import { tapOnboarding } from "./onboardingEvents.ts";
import { splitAgentHosts } from "./platformAgents.ts";
import { apiUrl } from "./config";
import { authedFetch, errorDetail } from "./session.ts";
import { downloadFilename, type RoomHandoffInfo, type RoomImportResult } from "./handoff.ts";
import type { Grant } from "./sessionGrant.ts";
import type { TenantPolicy } from "./shares.ts";
import type { RoomAgentCatalog } from "./agentCatalogs.ts";
import {
  teamAgentsPath,
  usageReportPath,
  type BorrowUsageReport,
  type CreateQuotaRequestInput,
  type QuotaRequest,
  type QuotaRequestStatus,
  type TeamAgentsReply,
  type TeamRange,
  type TeamScope,
  type UsageReportQuery,
} from "./teamDashboard.ts";
import type {
  Room,
  Channel,
  ChatAttachment,
  Member,
  TenantAuditRecord,
  TenantUsage,
  TenantMember,
  TenantSettings,
  PlatformTenant,
  UpdatePolicy,
  UpdateConfig,
  ReleaseFile,
  ReleaseEntry,
  ReleaseHubMeta,
  ReleaseHub,
  DirectoryTenant,
  JoinRequest,
  AgentJob,
  Artifact,
  ArtifactListFilter,
  ArtifactVersion,
  AgentTarget,
  Core,
} from "./apiModels";
import type {
  ComponentInstallRequest,
  ComponentKey,
  ComponentKind,
  ComponentRollout,
  MemberComponent,
  PlatformComponent,
  PublishedComponent,
} from "./apiTypes";
import type { NotificationTestResult } from "./notificationPrefs.ts";
import type { MessageTask, SearchResponse, TaskStatus } from "./teamModels";

// Thin fetch wrapper around the gateway /v1 + /api surface. Injects the session
// bearer token and centralizes error shaping so views get typed helpers.

export type {
  ComponentAdoption,
  ComponentArtifact,
  ComponentBase,
  ComponentEndpoint,
  ComponentInstallRequest,
  ComponentKey,
  ComponentKind,
  ComponentPackageEnvelope,
  ComponentPackageKind,
  ComponentPermissions,
  ComponentProtocolRange,
  ComponentRollout,
  ComponentRolloutRequest,
  ComponentRuntime,
  ComponentSidecar,
  ComponentTrust,
  ComponentValidationStep,
  ComponentValidationStepId,
  ComponentValidationStepState,
  ComponentPublishRequest,
  MemberComponent,
  LocalSettingField,
  PlatformComponent,
  PublishedComponent,
  CorePackageRuntime,
  CorePackageProtocolRange,
  CorePackageEndpoint,
  CorePackageArtifact,
  CorePackagePermissions,
  CorePackageKey,
  CorePackage,
} from "./apiTypes";
export type * from "./apiModels";
export type * from "./teamModels";

// req is exported for feature API modules (e.g. notificationsApi.ts) that keep
// their endpoints beside their own types instead of growing this file.
export async function req<T>(path: string, opts: RequestInit & { raw?: boolean } = {}): Promise<T> {
  const { raw, ...init } = opts;
  const headers = new Headers(init.headers);
  if (init.body) headers.set("Content-Type", "application/json");
  const res = await authedFetch(apiUrl(path), { ...init, headers });
  if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
  if (raw) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

function artifactListQuery(filter: ArtifactListFilter): string {
  const params = new URLSearchParams();
  for (const key of ["kind", "mime", "author", "q", "cursor"] as const) {
    const value = filter[key]?.trim();
    if (value) params.set(key, value);
  }
  if (filter.sourceSeq && filter.sourceSeq > 0) params.set("sourceSeq", String(filter.sourceSeq));
  if (filter.limit && filter.limit > 0) params.set("limit", String(filter.limit));
  const query = params.toString();
  return query ? `?${query}` : "";
}

function artifactVersionPath(roomId: string, artifactId: string, version: number): string {
  return `/v1/rooms/${encodeURIComponent(roomId)}/artifacts/${encodeURIComponent(artifactId)}/versions/${version}`;
}

async function fetchArtifactBlob(path: string): Promise<Blob> {
  const res = await authedFetch(apiUrl(path));
  if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
  return res.blob();
}

// A component is addressed by name AND kind: a core and a plugin may share a
// name, and an unqualified name that matches two kinds is a 409.
type ComponentRef = { readonly name: string; readonly kind: ComponentKind };
function componentPath(base: string, c: ComponentRef, suffix = ""): string {
  return `${base}/${encodeURIComponent(c.name)}${suffix}?kind=${encodeURIComponent(c.kind)}`;
}

// Upper bound of room ids per GET /v1/rooms-unread (the gateway rejects more).
const UNREAD_CHUNK = 200;

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

// scoped appends ?tenantId=/&tenantId= to a tenant-scoped path when a
// super-admin targets a specific org. The gateway ignores the param for
// non-super-admins, so it is always safe to send when set.
export function scoped(path: string, tenantId?: string): string {
  if (!tenantId) return path;
  const sep = path.includes("?") ? "&" : "?";
  return `${path}${sep}tenantId=${encodeURIComponent(tenantId)}`;
}

export const api = {
  login: (idpToken: string) =>
    req<Grant>("/v1/auth/login", {
      method: "POST",
      body: JSON.stringify({ idpToken }),
    }),
  // Renew a session from a refresh token (no bearer). The grant carries a
  // rotated refresh token that replaces the stored one; 401 = sign in again.
  refresh: (refreshToken: string) =>
    req<Grant>("/v1/auth/refresh", {
      method: "POST",
      body: JSON.stringify({ refreshToken }),
    }),
  // Revoke the refresh token's sign-in server-side (idempotent, 204).
  logout: (refreshToken: string) =>
    req<void>("/v1/auth/logout", {
      method: "POST",
      body: JSON.stringify({ refreshToken }),
      raw: true,
    }),
  // Any member of multiple tenants: re-issue the session scoped to another
  // tenant they belong to. The whole session (rooms, agents, role) follows.
  switchTenant: (tenantId: string) =>
    req<{ sessionToken: string; subject: string; tenant?: string; role?: string }>(
      "/v1/auth/switch-tenant",
      {
        method: "POST",
        body: JSON.stringify({ tenantId }),
      },
    ),
  // Platform super-admin: list every tenant (cross-tenant overview).
  listTenants: () => req<{ tenants: PlatformTenant[] }>("/v1/platform/tenants"),
  // Platform super-admin: create a new tenant.
  createTenant: (body: { tenantId: string; name?: string; ssoIssuer?: string }) =>
    req<PlatformTenant>("/v1/platform/tenants", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  // Platform super-admin: activate or suspend a tenant.
  setTenantStatus: (tenantId: string, status: "active" | "suspended") =>
    req<PlatformTenant>(`/v1/platform/tenants/${encodeURIComponent(tenantId)}/status`, {
      method: "PUT",
      body: JSON.stringify({ status }),
    }),
  getUpdatePolicy: () => req<UpdatePolicy>("/v1/app/update-policy"),
  getUpdateConfig: () => req<UpdateConfig>("/v1/platform/update-config"),
  setUpdateConfig: (body: UpdateConfig) =>
    req<UpdateConfig>("/v1/platform/update-config", {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  listPlatformComponents: () => req<{ components: PlatformComponent[] }>("/v1/platform/components"),
  publishComponent: (body: ComponentInstallRequest) =>
    req<PublishedComponent>("/v1/platform/components", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  uploadArtifact: (file: File, options: Omit<ArtifactUploadOptions, "fetchImpl" | "origin"> = {}) =>
    uploadBinaryArtifact(file, options),
  artifactUrl,
  setComponentRollout: (component: ComponentRef, rollout: ComponentRollout) =>
    req<void>(componentPath("/v1/platform/components", component, "/rollout"), {
      method: "PUT",
      body: JSON.stringify({ rollout }),
      raw: true,
    }),
  uninstallPlatformComponent: (component: ComponentRef) =>
    req<void>(componentPath("/v1/platform/components", component), { method: "DELETE", raw: true }),
  // Notifier components: a new signing secret (shown once) and a test send
  // to the calling super-admin through the notifier's default delivery.
  rotateComponentSecret: (component: ComponentRef) =>
    req<{ secret: string }>(componentPath("/v1/platform/components", component, "/secret/rotate"), { method: "POST" }),
  testComponent: (component: ComponentRef) =>
    req<NotificationTestResult>(componentPath("/v1/platform/components", component, "/test"), { method: "POST" }),
  // signatureRequired mirrors the gateway's install policy (false only on a dev
  // gateway with BNBLOC_EXT_ALLOW_UNSIGNED); absent is treated as required.
  listComponentKeys: () => req<{ keys: ComponentKey[]; signatureRequired?: boolean }>("/v1/platform/component-keys"),
  addComponentKey: (body: { readonly publicKey: string; readonly label?: string }) =>
    req<ComponentKey>("/v1/platform/component-keys", { method: "POST", body: JSON.stringify(body) }),
  removeComponentKey: (keyId: string) =>
    req<void>(`/v1/platform/component-keys/${encodeURIComponent(keyId)}`, { method: "DELETE", raw: true }),
  components: () => req<{ components: MemberComponent[] }>("/v1/components"),
  installComponent: (component: ComponentRef) =>
    req<void>(componentPath("/v1/components", component, "/install"), { method: "POST", raw: true }),
  uninstallComponent: (component: ComponentRef) =>
    req<void>(componentPath("/v1/components", component, "/install"), { method: "DELETE", raw: true }),
  // Trusted publisher keys: the root of trust for the component market. A package
  // signed by a key absent from this list is refused at install.
  // Any signed-in user: the in-app release hub payload (project intro + version
  // history + downloads). No host/data is baked into the client; it all comes
  // from here, so a super admin maintains it per instance.
  getAppReleases: () => req<ReleaseHub>("/v1/app/releases"),
  downloadReleaseFile: (file: ReleaseFile) => fetchReleaseFile(file.url, file.name, apiUrl("/")),
  // Super-admin: full history + intro for the editor, and its mutators.
  getPlatformReleases: () => req<ReleaseHub>("/v1/platform/app-releases"),
  putReleaseMeta: (body: ReleaseHubMeta) =>
    req<ReleaseHubMeta>("/v1/platform/app-releases/meta", {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  putRelease: (version: string, body: Omit<ReleaseEntry, "version">) =>
    req<ReleaseEntry>(`/v1/platform/app-releases/${encodeURIComponent(version)}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  deleteRelease: (version: string) =>
    req<void>(`/v1/platform/app-releases/${encodeURIComponent(version)}`, {
      method: "DELETE",
      raw: true,
    }),
  // Any user: browse joinable tenants with the caller's relation to each.
  tenantDirectory: () => req<{ tenants: DirectoryTenant[] }>("/v1/tenants/directory"),
  // Any user: submit (or re-submit) a request to join a tenant.
  requestJoin: (tenantId: string, message?: string) =>
    req<JoinRequest>(`/v1/tenants/${encodeURIComponent(tenantId)}/join-request`, {
      method: "POST",
      body: JSON.stringify({ message: message ?? "" }),
    }),
  // Any user: the caller's own join requests across tenants (status tracking).
  myJoinRequests: () => req<{ requests: JoinRequest[] }>("/v1/tenants/join-requests"),
  // Org-admin: pending join requests for the ctx tenant (status=all|approved|rejected widens).
  listJoinRequests: (status?: "pending" | "approved" | "rejected" | "all", tenantId?: string) =>
    req<{ requests: JoinRequest[] }>(
      scoped(`/v1/tenant/join-requests${status ? `?status=${status}` : ""}`, tenantId),
    ),
  // Org-admin: approve a pending request (creates membership).
  approveJoinRequest: (userId: string, tenantId?: string) =>
    req<JoinRequest>(scoped(`/v1/tenant/join-requests/${encodeURIComponent(userId)}/approve`, tenantId), {
      method: "POST",
    }),
  // Org-admin: reject a pending request.
  rejectJoinRequest: (userId: string, tenantId?: string) =>
    req<JoinRequest>(scoped(`/v1/tenant/join-requests/${encodeURIComponent(userId)}/reject`, tenantId), {
      method: "POST",
    }),
  // Org-admin: the tenant's audit trail across all its rooms (newest-first).
  // A super-admin passes tenantId to target another org; org-admins omit it and
  // stay scoped to their own tenant (the gateway ignores the param for them).
  tenantAudit: (limit = 100, tenantId?: string) =>
    req<{ records: TenantAuditRecord[] }>(scoped(`/v1/tenant/audit?limit=${limit}`, tenantId)),
  // Org-admin: the tenant's aggregate agent usage + configured ceilings.
  tenantUsage: (tenantId?: string) => req<TenantUsage>(scoped("/v1/tenant/usage", tenantId)),
  // Org-admin: the tenant's members with their org roles.
  tenantMembers: (tenantId?: string) =>
    req<{ members: TenantMember[] }>(scoped("/v1/tenant/members", tenantId)),
  // Org-admin: add (or upsert) a member by subject id with a chosen role.
  addTenantMember: (userId: string, role: "org_admin" | "member" | "viewer", tenantId?: string) =>
    req<TenantMember>(scoped("/v1/tenant/members", tenantId), {
      method: "POST",
      body: JSON.stringify({ userId, role }),
    }),
  // Org-admin: remove a member (cannot remove self or the last admin).
  removeTenantMember: (userId: string, tenantId?: string) =>
    req<{ removed: boolean; userId: string }>(
      scoped(`/v1/tenant/members/${encodeURIComponent(userId)}`, tenantId),
      { method: "DELETE" },
    ),
  // Org-admin: change one member's org role (cannot target yourself).
  setTenantMemberRole: (userId: string, role: "org_admin" | "member" | "viewer", tenantId?: string) =>
    req<TenantMember>(scoped(`/v1/tenant/members/${encodeURIComponent(userId)}/role`, tenantId), {
      method: "PUT",
      body: JSON.stringify({ role }),
    }),
  // Org-admin: persist org-level run/cost ceilings (0 = unlimited).
  setTenantQuota: (runQuota: number, costAllowance: number, tenantId?: string) =>
    req<{ runQuota: number; costAllowance: number }>(scoped("/v1/tenant/quota", tenantId), {
      method: "PUT",
      body: JSON.stringify({ runQuota, costAllowance }),
    }),
  // Org-admin: read/update the tenant settings (display name, SSO issuer).
  tenantSettings: (tenantId?: string) => req<TenantSettings>(scoped("/v1/tenant/settings", tenantId)),
  updateTenantSettings: (
    body: { name?: string; ssoIssuer?: string; requireSandboxForBorrow?: boolean; publicSharesDisabled?: boolean },
    tenantId?: string,
  ) =>
    req<TenantSettings>(scoped("/v1/tenant/settings", tenantId), {
      method: "PUT",
      body: JSON.stringify(body),
    }),
  // The tenant policy every member reads: the agent-sandbox rule (the
  // settings sandbox section explains it; the owner's bridge enforces it) and
  // whether public share links are off (the share actions hide). Read through
  // lib/tenantPolicy.ts, which caches it per tenant.
  myTenantPolicy: (tenantId?: string) =>
    req<TenantPolicy>(`/v1/my/tenant-policy${tenantId ? `?tenant=${encodeURIComponent(tenantId)}` : ""}`),
  ssoConfig: () => req<{ authProviderAvailable?: boolean; authProviderStartPath?: string; directLoginAvailable?: boolean }>("/v1/auth/sso-config"),
  // Redeem a desktop SSO hand-off code with the nonce the app started the
  // flow with (single use, ~60s TTL).
  ssoExchange: (code: string, nonce: string) =>
    req<Grant>("/v1/auth/sso/exchange", {
      method: "POST",
      body: JSON.stringify({ code, nonce }),
    }),
  cores: () => req<{ cores: Core[] }>("/v1/cores"),

  // Chat messages as tasks (F6): any room member may mark/toggle/unmark.
  roomTasks: (roomId: string) => req<{ tasks: MessageTask[] }>(`/v1/rooms/${encodeURIComponent(roomId)}/tasks`),
  setTask: (roomId: string, seq: number, status: TaskStatus) =>
    req<MessageTask>(`/v1/rooms/${encodeURIComponent(roomId)}/tasks/${seq}`, {
      method: "PUT",
      body: JSON.stringify({ status }),
    }),
  unmarkTask: (roomId: string, seq: number) =>
    req<void>(`/v1/rooms/${encodeURIComponent(roomId)}/tasks/${seq}`, { method: "DELETE", raw: true }),
  // Global search (F7) across the caller's rooms (resolved server-side).
  search: (q: string, opts: { before?: number; limit?: number } = {}) => {
    const p = new URLSearchParams({ q });
    if (opts.before) p.set("before", String(opts.before));
    if (opts.limit) p.set("limit", String(opts.limit));
    return req<SearchResponse>(`/v1/search?${p.toString()}`);
  },
  myRooms: (subject: string) =>
    req<{ rooms: Room[] }>(
      `/v1/users/${encodeURIComponent(subject)}/rooms?detail=1`,
    ),
  searchRooms: (q: string) =>
    req<{ rooms: Room[] }>(`/v1/rooms?q=${encodeURIComponent(q)}`),
  getRoom: (id: string) => req<Room>(`/v1/rooms/${encodeURIComponent(id)}`),
  createRoom: (body: {
    roomId: string;
    name?: string;
    description?: string;
    visibility: string;
  }) => req<Room>("/v1/rooms", { method: "POST", body: JSON.stringify(body) }),
  updateRoom: (
    id: string,
    body: {
      name?: string;
      description?: string;
      visibility?: string;
      syncMode?: string;
    },
  ) =>
    req<Room>(`/v1/rooms/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    }),
  deleteRoom: (id: string) =>
    req<void>(`/v1/rooms/${encodeURIComponent(id)}`, {
      method: "DELETE",
      raw: true,
    }),
  joinRoom: (id: string) =>
    req<void>(`/v1/rooms/${encodeURIComponent(id)}/join`, {
      method: "POST",
      raw: true,
    }),
  leaveRoom: (id: string) =>
    req<void>(`/v1/rooms/${encodeURIComponent(id)}/leave`, {
      method: "POST",
      raw: true,
    }),
  // The room's people. Agent-host memberships (a platform agent's host) are
  // split off here, the one place, so no people list ever shows them.
  members: async (id: string) => {
    const res = await req<{ members: Member[] | null }>(`/v1/rooms/${encodeURIComponent(id)}/members`);
    const { people, agentHosts } = splitAgentHosts(res.members ?? []);
    return { members: people, agentHosts };
  },
  // Remove a member from a room (owner/admin only; cannot remove the owner or
  // yourself — use leave for the latter).
  removeMember: (id: string, userId: string) =>
    req<void>(
      `/v1/rooms/${encodeURIComponent(id)}/members/${encodeURIComponent(userId)}`,
      { method: "DELETE", raw: true },
    ),
  // Per-room unread counts for the caller; pass the known room ids. The
  // gateway bounds one request's room list, so larger sets are fetched in
  // chunks and merged.
  roomsUnread: async (roomIds: string[]) => {
    const merged: {
      unread: Record<string, number>;
      // Per-channel unread counts per room (only channels with unread > 0,
      // default first).
      channels: Record<string, { channelId: string; count: number }[]>;
    } = { unread: {}, channels: {} };
    for (let i = 0; i < roomIds.length; i += UNREAD_CHUNK) {
      const part = await req<{
        unread?: Record<string, number>;
        channels?: Record<string, { channelId: string; count: number }[]>;
      }>(`/v1/rooms-unread?rooms=${encodeURIComponent(roomIds.slice(i, i + UNREAD_CHUNK).join(","))}`);
      Object.assign(merged.unread, part.unread ?? {});
      Object.assign(merged.channels, part.channels ?? {});
    }
    return merged;
  },
  // Advance the caller's last-read sequence in a room (clears its unread).
  // channelId scopes the cursor to one channel; absent = the room default.
  markRead: (id: string, seq: number, channelId?: string) =>
    req<void>(`/v1/rooms/${encodeURIComponent(id)}/read`, {
      method: "POST",
      body: JSON.stringify({ seq, ...(channelId ? { channelId } : {}) }),
      raw: true,
    }),
  // Emit a transient typing hint to the room (ephemeral; not persisted).
  roomTyping: (id: string, channelId?: string) =>
    req<void>(`/v1/rooms/${encodeURIComponent(id)}/typing`, {
      method: "POST",
      body: JSON.stringify({ ...(channelId ? { channelId } : {}) }),
      raw: true,
    }),
  // Paginated room-feed history older than `before` (a sequence cursor; omit for
  // the latest page), ascending by sequence. Beyond the live replay buffer.
  roomHistory: (id: string, before?: number, limit = 50, channelId?: string) =>
    req<{
      messages: {
        sequence: number;
        kind: string;
        channelId?: string;
        payload: Record<string, unknown>;
        atUnixMs: number;
      }[];
    }>(
      `/v1/rooms/${encodeURIComponent(id)}/history?limit=${limit}${before ? `&before=${before}` : ""}${channelId ? `&channelId=${encodeURIComponent(channelId)}` : ""}`,
    ),
  // Durable agent jobs for the current room/channel, used to show queued,
  // running, and failed state below the originating chat message.
  agentJobs: (roomId: string, channelId?: string) =>
    req<{ jobs: AgentJob[] }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/agent-jobs${channelId ? `?channelId=${encodeURIComponent(channelId)}` : ""}`,
    ),
  // Stop a queued or running job. Authorized by room membership, so a member can
  // stop a runaway turn even when another member's bridge holds the lease.
  cancelAgentJob: (roomId: string, jobId: string) =>
    req<{ jobId: string; status: string }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/agent-jobs/${encodeURIComponent(jobId)}/cancel`,
      { method: "POST", body: JSON.stringify({}) },
    ),

  // Room artifacts: create / list / read / publish under the room's membership.
  createArtifact: (roomId: string, body: { kind: string; title: string; channelId?: string; language?: string; content?: string; summary?: string }) =>
    req<{ artifact: Artifact }>(`/v1/rooms/${encodeURIComponent(roomId)}/artifacts`, {
      method: "POST",
      body: JSON.stringify(body),
    }),
  // The room's artifacts, optionally filtered server-side (kind / mime /
  // author / producing message / text search) and paged (nextCursor).
  artifacts: (roomId: string, filter: ArtifactListFilter = {}) =>
    req<{ artifacts: Artifact[]; nextCursor?: string }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/artifacts${artifactListQuery(filter)}`,
    ),
  // The deliverables produced by the room messages with seq in [from, to]:
  // the feed anchors its cards under the messages it has loaded, however old.
  artifactAnchors: (roomId: string, from: number, to: number) =>
    req<{ anchors: { seq: number; artifactId: string }[] }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/artifact-anchors?from=${from}&to=${to}`,
    ),
  artifactDetail: (roomId: string, artifactId: string) =>
    req<{ artifact: Artifact; versions: ArtifactVersion[] }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/artifacts/${encodeURIComponent(artifactId)}`,
    ),
  artifactVersion: (roomId: string, artifactId: string, version: number) =>
    req<{ version: ArtifactVersion }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/artifacts/${encodeURIComponent(artifactId)}/versions/${version}`,
    ),
  // Relay a live artifact's collaboration state (a gesture in a running
  // preview) to the room. Transient: it fans out to current viewers, it is
  // never a document revision.
  // Join snapshot: the artifact's last relayed live state, delivered to a
  // member starting or refreshing a preview so late joiners land on the
  // current board. undefined = no live session yet.
  getArtifactState: (roomId: string, artifactId: string) =>
    req<{ state: unknown; actor: string; atUnixMs: number } | undefined>(
      `/v1/rooms/${encodeURIComponent(roomId)}/artifacts/${encodeURIComponent(artifactId)}/state`,
    ),
  artifactState: (roomId: string, artifactId: string, state: unknown) =>
    req<void>(`/v1/rooms/${encodeURIComponent(roomId)}/artifacts/${encodeURIComponent(artifactId)}/state`, {
      method: "POST",
      body: JSON.stringify({ artifactId, state }),
    }),
  publishArtifactVersion: (
    roomId: string,
    artifactId: string,
    body: { baseVersion: number; content: string; summary?: string },
  ) =>
    req<{ artifact: Artifact; version: ArtifactVersion }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/artifacts/${encodeURIComponent(artifactId)}/versions`,
      { method: "POST", body: JSON.stringify(body) },
    ),
  // One version's raw bytes (inline text or blob-backed file), fetched with
  // the session bearer so viewers render from a local blob URL.
  artifactContent: (roomId: string, artifactId: string, version: number) =>
    fetchArtifactBlob(`${artifactVersionPath(roomId, artifactId, version)}/content`),
  // The stored thumbnail of one version (present when version.thumbnail is).
  artifactThumbnail: (roomId: string, artifactId: string, version: number) =>
    fetchArtifactBlob(`${artifactVersionPath(roomId, artifactId, version)}/thumbnail`),
  // Publish a blob-backed version (multipart): a file deliverable, or the
  // "restore as new version" of an older file version.
  publishArtifactFile: async (
    roomId: string,
    artifactId: string,
    body: { file: Blob; fileName: string; baseVersion: number; summary?: string; sourceJobId?: string; sourceSeq?: number },
  ): Promise<{ artifact: Artifact; version: ArtifactVersion }> => {
    const form = new FormData();
    form.append("file", body.file, body.fileName);
    form.append("baseVersion", String(body.baseVersion));
    if (body.summary) form.append("summary", body.summary);
    if (body.sourceJobId) form.append("sourceJobId", body.sourceJobId);
    if (body.sourceSeq) form.append("sourceSeq", String(body.sourceSeq));
    const res = await authedFetch(
      apiUrl(`/v1/rooms/${encodeURIComponent(roomId)}/artifacts/${encodeURIComponent(artifactId)}/versions`),
      { method: "POST", body: form },
    );
    if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
    return (await res.json()) as { artifact: Artifact; version: ArtifactVersion };
  },

  // Room channels: list (default first), create (any member), rename
  // (owner/admin or creator; the default channel is immutable), delete
  // (owner/admin; the default channel is undeletable).
  channels: (roomId: string) =>
    req<{ channels: Channel[] }>(`/v1/rooms/${encodeURIComponent(roomId)}/channels`),
  createChannel: (roomId: string, name: string) =>
    req<Channel>(`/v1/rooms/${encodeURIComponent(roomId)}/channels`, {
      method: "POST",
      body: JSON.stringify({ name }),
    }),
  renameChannel: (roomId: string, channelId: string, name: string) =>
    req<Channel>(
      `/v1/rooms/${encodeURIComponent(roomId)}/channels/${encodeURIComponent(channelId)}`,
      { method: "PATCH", body: JSON.stringify({ name }) },
    ),
  // The room's people chat: a human-only side conversation agents never see.
  // peopleChat reads it (404 until a member first opens it); openPeopleChat
  // creates it on first open (idempotent, any member).
  peopleChat: (roomId: string) =>
    req<Channel>(`/v1/rooms/${encodeURIComponent(roomId)}/people-chat`),
  openPeopleChat: (roomId: string) =>
    req<Channel>(`/v1/rooms/${encodeURIComponent(roomId)}/people-chat`, { method: "POST" }),
  deleteChannel: (roomId: string, channelId: string) =>
    req<{ deleted: boolean }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/channels/${encodeURIComponent(channelId)}`,
      { method: "DELETE" },
    ),
  transferOwner: (id: string, userId: string) =>
    req<void>(`/v1/rooms/${encodeURIComponent(id)}/transfer`, {
      method: "POST",
      body: JSON.stringify({ userId }),
      raw: true,
    }),
  createInvite: (id: string, body: { role?: string; capacity?: number; expiresAtMs?: number }) =>
    req<{ code: string }>(`/v1/rooms/${encodeURIComponent(id)}/invites`, {
      method: "POST",
      body: JSON.stringify(body),
    }).then(tapOnboarding<{ code: string }>({ kind: "invite_created", roomId: id })),
  redeemInvite: (code: string) =>
    req<{ roomId: string }>(`/v1/invites/${encodeURIComponent(code)}/redeem`, {
      method: "POST",
      raw: true,
    }),
  // Delete a room message you authored (soft delete; broadcast to the room).
  deleteMessage: (id: string, seq: number) =>
    req<void>(
      `/v1/rooms/${encodeURIComponent(id)}/messages/${seq}`,
      { method: "DELETE", raw: true },
    ),
  // Edit a room message you authored (only the author, and not if deleted).
  // Broadcasts a message_edited event to the room.
  editMessage: (id: string, seq: number, text: string) =>
    req<void>(
      `/v1/rooms/${encodeURIComponent(id)}/messages/${seq}`,
      { method: "PATCH", body: JSON.stringify({ text }), raw: true },
    ),
  // Add (or upsert the role of) a member by id (owner/admin only). Reuses the
  // members upsert endpoint, which updates role on conflict.
  addMember: (id: string, userId: string, role: string) =>
    req<void>(`/v1/rooms/${encodeURIComponent(id)}/members`, {
      method: "POST",
      body: JSON.stringify({ userId, role }),
      raw: true,
    }).then(tapOnboarding<void>({ kind: "invite_created", roomId: id })),
  // Borrowable agents active in a room (non-revoked, non-expired), so browser
  // users without a local core can discover agents to borrow.
  sharedAgents: (id: string) =>
    req<{
      targetId: string;
      ownerUserId: string;
      provider: string;
      allowedModels: string[];
      hasQuota: boolean;
    }[]>(`/v1/rooms/${encodeURIComponent(id)}/shared-agents`),
  // The model catalogs of the agents addressable in a room, keyed by owner
  // (owner, core, provider): members' live agents and the room's platform
  // agents. The composer's model picker reads a recipient's catalog here.
  roomAgentCatalogs: (id: string) =>
    req<{ agents: RoomAgentCatalog[] }>(`/v1/rooms/${encodeURIComponent(id)}/agent-catalogs`),
  // Live room agent roster: the union of the agents every member's bridge
  // advertises as reachable here. Remote-safe — unlike localAgentTargets it
  // does not require the gateway to reach the caller's machine, so a browser
  // user against a remote gateway still sees drivable agents.
  roomAgents: (id: string) =>
    req<{ agents: { ownerUserId: string; core?: string; provider: string; sandboxed?: boolean }[] }>(
      `/v1/rooms/${encodeURIComponent(id)}/agents`,
    ),
  // Multipart upload: the browser sets the multipart Content-Type boundary, so
  // this bypasses req's JSON header but shares its authenticated transport.
  uploadAttachment: async (roomId: string, file: File): Promise<ChatAttachment> => {
    const form = new FormData();
    form.append("file", file);
    const res = await authedFetch(apiUrl(`/v1/rooms/${encodeURIComponent(roomId)}/attachments`), {
      method: "POST",
      body: form,
    });
    if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
    return (await res.json()) as ChatAttachment;
  },
  // Handoff package (owner/admin): download the room's export zip. The server
  // streams the archive, so its size is unknown up front; onProgress gets the
  // bytes received so far.
  exportRoomPackage: async (
    roomId: string,
    onProgress?: (bytes: number) => void,
  ): Promise<{ blob: Blob; filename: string }> => {
    const res = await authedFetch(apiUrl(`/v1/rooms/${encodeURIComponent(roomId)}/export`));
    if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
    const filename = downloadFilename(res.headers.get("Content-Disposition"), roomId);
    if (!res.body) return { blob: await res.blob(), filename };
    const reader = res.body.getReader();
    const chunks: BlobPart[] = [];
    let received = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      chunks.push(value as BlobPart);
      received += value.byteLength;
      onProgress?.(received);
    }
    return { blob: new Blob(chunks, { type: "application/zip" }), filename };
  },
  // Import a handoff package into a NEW room owned by the caller.
  importRoomPackage: async (file: File): Promise<RoomImportResult> => {
    const form = new FormData();
    form.append("file", file);
    const res = await authedFetch(apiUrl("/v1/rooms/import"), { method: "POST", body: form });
    if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
    return (await res.json()) as RoomImportResult;
  },
  // A room's import provenance; 404 (ApiError) for a room that was not imported.
  roomHandoff: (roomId: string) =>
    req<RoomHandoffInfo>(`/v1/rooms/${encodeURIComponent(roomId)}/handoff`),
  // A short-lived ticket URL for one attachment (an <img>/<a download>
  // cannot carry the bearer); lib/attachmentTickets.ts caches and renews it.
  attachmentTicket: (roomId: string, attachmentId: string) =>
    req<{ url: string; expiresAtMs: number }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/attachments/${encodeURIComponent(attachmentId)}/ticket`,
      { method: "POST" },
    ),
  // Set a member's role (owner/admin only; cannot target the owner or grant
  // owner). Reuses the add-member upsert, which updates role on conflict.
  setMemberRole: (id: string, userId: string, role: "admin" | "member" | "visitor") =>
    req<void>(`/v1/rooms/${encodeURIComponent(id)}/members`, {
      method: "POST",
      body: JSON.stringify({ userId, role }),
      raw: true,
    }),
  // Post a chat message. The answer carries the sequence the server assigned
  // (also for an idempotent retry of an already-stored clientMsgId), which is
  // how the optimistic row is reconciled with the stream echo.
  sendChat: (
    id: string,
    message: {
      readonly text: string;
      readonly recipient: {
        readonly ownerId: string;
        readonly provider: string;
        readonly core?: string;
        readonly agentOptions?: { readonly model?: string; readonly reasoningEffort?: string };
      } | null;
      readonly channelId: string | null;
      readonly attachmentIds: readonly string[];
      // The expert the agent recipient is asked to wear.
      readonly expert?: { readonly id: string; readonly version?: string };
    },
    clientMsgId: string,
  ) =>
    req<{ sequence: number }>(`/v1/rooms/${encodeURIComponent(id)}/chat`, {
      method: "POST",
      body: JSON.stringify({
        text: message.text,
        atUnixMs: Date.now(),
        clientMsgId,
        ...(message.channelId ? { channelId: message.channelId } : {}),
        ...(message.recipient ? { recipient: message.recipient } : {}),
        ...(message.attachmentIds.length > 0 ? { attachmentIds: message.attachmentIds } : {}),
        ...(message.recipient && message.expert ? { expert: message.expert } : {}),
      }),
    }),
  // The caller's own bridge-advertised agents (all rooms): the "my agents"
  // surface rendered identically on desktop and in the browser.
  myAgents: () =>
    req<{
      agents: {
        core: string;
        provider: string;
        targetId: string;
        online: boolean;
        // The bridge-uploaded model catalog (same shape as AgentTarget's
        // pickers): models, reasoningEfforts, reasoningByModel, defaults.
        modelOptions?: {
          models?: string[];
          reasoningEfforts?: string[];
          reasoningByModel?: Record<string, string[]>;
          defaultModel?: string;
          defaultReasoning?: string;
        };
      }[];
      // The member's own desktop app, apart from its agents: online = its
      // bridge is connected now (even with no agent core); lastSeenAtMs = the
      // latest connection, 0 when the app never connected (not installed).
      desktop: { online: boolean; lastSeenAtMs: number };
    }>("/v1/my/agents"),
  // Per-room+channel agent selection (restored by the composer on entry).
  getAgentPrefs: (roomId: string, channelId?: string) =>
    req<{ roomId: string; channelId: string; core: string; provider: string; model: string; reasoning: string; found: boolean }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/agent-prefs${channelId ? `?channel=${encodeURIComponent(channelId)}` : ""}`,
    ),
  putAgentPrefs: (roomId: string, body: { channelId?: string; core: string; provider: string; model?: string; reasoning?: string }) =>
    req<void>(`/v1/rooms/${encodeURIComponent(roomId)}/agent-prefs`, {
      method: "PUT",
      body: JSON.stringify(body),
      raw: true,
    }),
  localAgentTargets: (localAgentUrl?: string, localAgentToken?: string) => {
    const q = new URLSearchParams();
    if (localAgentUrl) q.set("localAgentUrl", localAgentUrl);
    if (localAgentToken) q.set("localAgentToken", localAgentToken);
    return req<{ targets: AgentTarget[] }>(`/v1/local-agent/targets?${q}`);
  },
  localAgentDiscover: () =>
    req<{ found: boolean; localAgentUrl?: string; localAgentToken?: string }>(
      "/v1/local-agent/discover",
    ),
  runLocalAgent: (body: {
    prompt: string;
    agentTargetId?: string;
    core?: string;
    localAgentUrl?: string;
    localAgentToken?: string;
  }) =>
    req<{ roomId: string; agentSessionId: string }>("/v1/local-agent/run", {
      method: "POST",
      body: JSON.stringify(body),
    }),
  // Shared-agent borrow authorization (owner pre-authorization model). The
  // owner authorizes room-wide borrowing of one of their agents (a provider);
  // the driver (bridge) and the borrower's send path consult it. A grantee can
  // never authorize itself: the authorizing owner is the authenticated caller.
  authorizeBorrow: (
    roomId: string,
    target: string,
    allowedModels: string[],
    ttlMs?: number,
  ) =>
    req<{ authorized: boolean; expiresAtMs: number }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/shared-agents/${encodeURIComponent(target)}/authorization`,
      {
        method: "PUT",
        body: JSON.stringify({ allowedModels, ttlMs }),
      },
    ),
  deauthorizeBorrow: (roomId: string, target: string) =>
    req<{ authorized: boolean }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/shared-agents/${encodeURIComponent(target)}/authorization`,
      { method: "DELETE" },
    ),
  borrowAuthorization: (roomId: string, target: string, owner: string) =>
    req<{
      authorized: boolean;
      expiresAtMs?: number;
      // Present only for a run-limited authorization (F3 quota view).
      runQuota?: number;
      runsUsed?: number;
      quotaExhausted?: boolean;
    }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/shared-agents/${encodeURIComponent(target)}/authorization?owner=${encodeURIComponent(owner)}`,
    ),
  // Owner accountability view: the caller's live authorizations in a room with
  // usage and recent borrowers.
  borrowUsage: (roomId: string) =>
    req<{
      owner: string;
      authorizations: {
        provider: string;
        expiresAtMs: number;
        runsUsed: number;
        runQuota?: number;
        recentBorrowers: string[];
      }[];
    }>(
      `/v1/rooms/${encodeURIComponent(roomId)}/shared-agents-usage`,
    ),
  // Team agent dashboard (F1): the caller's own agents ("self") or, for org
  // admins, every member's ("tenant", gated on manage_org by the gateway).
  teamAgents: (scope: TeamScope, range: TeamRange) =>
    req<TeamAgentsReply>(teamAgentsPath(scope, range, new Date().getTimezoneOffset())),
  // Borrow billing (F3): grouped usage report for one month, and its CSV export.
  borrowUsageReport: (q: UsageReportQuery) => {
    const path = usageReportPath(q, new Date().getTimezoneOffset());
    if (!path) return Promise.reject(new ApiError(400, "Pick a month"));
    return req<BorrowUsageReport>(path);
  },
  downloadBorrowUsageCsv: async (q: UsageReportQuery) => {
    const path = usageReportPath(q, new Date().getTimezoneOffset(), "csv");
    if (!path) throw new ApiError(400, "Pick a month");
    const res = await authedFetch(apiUrl(path));
    if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
    saveBlob(await res.blob(), `borrow-usage-${q.as}-${q.month}.csv`);
  },
  // Borrow quota requests (F3). The requester / deciding owner is always the
  // signed-in member; only the request's owner may approve or decline.
  createQuotaRequest: (input: CreateQuotaRequestInput) =>
    req<{ request: QuotaRequest }>("/v1/borrow/quota-requests", {
      method: "POST",
      body: JSON.stringify(input),
    }),
  quotaRequests: (role: "owner" | "requester", opts: { status?: QuotaRequestStatus; roomId?: string } = {}) => {
    const params = new URLSearchParams({ role });
    if (opts.status) params.set("status", opts.status);
    if (opts.roomId) params.set("roomId", opts.roomId);
    return req<{ requests: QuotaRequest[] }>(`/v1/borrow/quota-requests?${params}`);
  },
  approveQuotaRequest: (requestId: string, runs?: number) =>
    req<{ request: QuotaRequest; runQuota?: number; runsUsed: number }>(
      `/v1/borrow/quota-requests/${encodeURIComponent(requestId)}/approve`,
      { method: "POST", body: JSON.stringify(runs ? { runs } : {}) },
    ),
  declineQuotaRequest: (requestId: string) =>
    req<{ request: QuotaRequest }>(`/v1/borrow/quota-requests/${encodeURIComponent(requestId)}/decline`, {
      method: "POST",
    }),
};
