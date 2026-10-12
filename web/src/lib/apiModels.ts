// Gateway DTOs (wire shapes) of the /v1 surface. Re-exported by api.ts, the
// client façade, so views import them from "@/lib/api". A shape the Go
// server owns is generated (wire.gen.ts) and only re-exported here; what
// stays below has no Go wire type yet.
export type {
  AgentJob,
  AgentTarget,
  Artifact,
  ArtifactThumbnail,
  ArtifactVersion,
  Core,
  PlazaRoom,
  RoomDetail,
  RoomMember as Member,
  RoomPreview,
  RoomSummary,
} from "./wire.gen.ts";
import type { RoomDetail, RoomPreview } from "./wire.gen.ts";

// Room is GET /v1/rooms/{id}: a member's detail, or a non-member's preview
// of a shared room (`"preview" in room` tells them apart).
export type Room = RoomDetail | RoomPreview;
// One channel of a room: a topic-scoped message stream. Members, agent
// borrows, and files are room-level and shared across channels.
export interface Channel {
  channelId: string;
  name: string;
  isDefault: boolean;
  // "people" for the room's people chat (human-only; agents never see it).
  // The channel list never contains it — it is reached via peopleChat.
  kind?: "people";
  createdBy: string;
}

export interface ChatAttachment {
  readonly id: string;
  readonly filename: string;
  readonly contentType: string;
  readonly size: number;
}

export interface TurnMetrics {
  readonly model?: string;
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly cacheCreationTokens: number;
  readonly cacheReadTokens: number;
  readonly thinkingTokens: number;
  readonly durationApiMs: number;
  readonly ttftMs: number;
  // Absent unless reported (an unreported cost is not $0).
  readonly costUsd?: number;
  readonly reported: boolean;
}

// Org-admin: one audit-trail entry for the tenant (agent borrow/turn decisions,
// and room/login lifecycle where emitted).
export interface TenantAuditRecord {
  roomId: string;
  actorUserId: string;
  ownerUserId: string;
  action: string;
  decision: string;
  denyReason?: string;
  model?: string;
  atMs: number;
}

// Org-admin: aggregate agent usage for the tenant plus configured ceilings
// (0 = unlimited).
export interface TenantUsage {
  runsUsed: number;
  costUsed: number;
  runQuota: number;
  costAllowance: number;
}

// Org-admin: one tenant member with their org role.
export interface TenantMember {
  userId: string;
  role: "org_admin" | "member" | "viewer";
  joinedAtMs: number;
  // Who owns the membership: an admin ("manual") or organization sync rules.
  source?: "manual" | "sync";
}

// Org-admin: the tenant registry row surfaced by the settings tab.
export interface TenantSettings {
  tenantId: string;
  name: string;
  status: string;
  ssoIssuer: string;
  // Org policy: borrowed agents must run in their owner's agent sandbox
  // (refused when the owner's sandbox is off or not ready).
  requireSandboxForBorrow: boolean;
  // Org policy: public share links are off (existing links answer 404, the
  // share actions are hidden).
  publicSharesDisabled: boolean;
}

// Platform super-admin: one tenant row in the cross-tenant overview.
export interface PlatformTenant {
  tenantId: string;
  name: string;
  status: string;
  ssoIssuer: string;
  runQuota: number;
  costAllowance: number;
  createdAtMs: number;
}

export interface UpdatePolicy {
  readonly enabled: boolean;
  readonly channel: "stable" | "internal";
  readonly autoCheck: boolean;
  readonly checkIntervalHours: number;
  readonly autoInstall: boolean;
  readonly minSupportedVersion: string;
}

export interface UpdatePlatformArtifact {
  readonly url: string;
  readonly signature: string;
}

export interface UpdateRelease {
  readonly version: string;
  readonly notes: string;
  readonly pubDate: string;
  readonly platforms: Readonly<Record<string, UpdatePlatformArtifact>>;
}

export interface UpdateConfig {
  readonly policy: UpdatePolicy;
  readonly release: UpdateRelease;
}

export interface ReleaseFile {
  readonly platform: string;
  readonly arch: string;
  readonly target?: string;
  readonly name: string;
  readonly url: string;
  readonly sha256?: string;
  readonly size?: number;
}

export interface ReleaseEntry {
  readonly version: string;
  readonly channel: string;
  readonly releaseDate: string;
  readonly notes: string;
  readonly files: readonly ReleaseFile[];
  readonly createdAtMs?: number;
  readonly createdBy?: string;
}

export interface ReleaseHubMeta {
  readonly name: string;
  readonly tagline: string;
  readonly intro: string;
}

export interface ReleaseHub {
  readonly project: ReleaseHubMeta;
  readonly releases: readonly ReleaseEntry[];
}

export interface DirectoryTenant {
  tenantId: string;
  name: string;
  relation: "member" | "pending" | "rejected" | "none";
}

// A membership join request and its lifecycle state.
export interface JoinRequest {
  tenantId: string;
  userId: string;
  message: string;
  status: "pending" | "approved" | "rejected";
  createdAtMs: number;
  decidedAtMs: number;
  decidedBy: string;
}

// Server-side filters of the room's artifact list (all optional).
export interface ArtifactListFilter {
  kind?: string;
  mime?: string;
  author?: string;
  sourceSeq?: number;
  q?: string;
  limit?: number;
  cursor?: string;
}

