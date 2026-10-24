// Gateway DTOs (wire shapes) of the /v1 surface. Re-exported by api.ts, the
// client façade, so views import them from "@/lib/api". A shape the Go
// server owns is generated (wire.gen.ts) and only re-exported here; what
// stays below has no Go wire type yet.
export type {
  AgentJob,
  AgentTarget,
  Artifact,
  ArtifactVersion,
  ChatAttachment,
  Core,
  PlazaRoom,
  PublishApproval,
  RoomDetail,
  RoomMember as Member,
  RoomPreview,
  RoomSummary,
} from "./wire.gen.ts";
import type { PublishApproval, RoomDetail, RoomPreview } from "./wire.gen.ts";

export type { UpdatePlatform as UpdatePlatformArtifact } from "./wire.gen.ts";

export type { ReleaseEntry, ReleaseFile, ReleaseHub, ReleaseHubMeta, UpdateConfig, UpdatePolicy } from "./wire.gen.ts";

// Room is GET /v1/rooms/{id}: a member's detail, or a non-member's preview
// of a shared room, which they read before joining (isPreviewRoom tells them
// apart: the ONE place that reads the discriminator).
export type Room = RoomDetail | RoomPreview;
export function isPreviewRoom(room: Room | null | undefined): room is RoomPreview {
  return !!room && "preview" in room;
}
// joinNeedsInviteCode: a person who is no member of a private room joins it
// with an invite code (a shared room joins with a click).
export function joinNeedsInviteCode(room: Room | null | undefined): boolean {
  return isPreviewRoom(room) && room.visibility === "private";
}
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

export interface TurnMetrics {
  readonly model?: string;
  readonly inputTokens: number;
  readonly outputTokens: number;
  readonly cacheCreationTokens: number;
  readonly cacheReadTokens: number;
  readonly thinkingTokens: number;
  readonly durationApiMs: number;
  readonly ttftMs: number;
  // How full the agent's conversation is after this reply: the tokens the model
  // read for the turn's last request, and the model's window. 0 = not reported;
  // read whether or not the tokens and cost were (docs/architecture/core-providers.md).
  readonly contextTokens: number;
  readonly contextWindow: number;
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
  // Org policy: share links for people outside the organization (anyone
  // signed in, and anyone with the link) are off; existing ones answer 404.
  outsideLinksDisabled: boolean;
  // Org policy: share links anyone can open without signing in are off
  // (existing ones answer 404). Links for the organization stay allowed
  // whatever the two switches say.
  anonymousLinksDisabled: boolean;
  // Org policy: whether a member's publish to the organization waits for
  // an admin ("required", the default) or takes effect at once ("none").
  publishApproval: PublishApproval;
  // Org policy: AI assist is off for the organization's members (the
  // assist buttons are hidden; docs/prd/2026-10-13-ai-assist.md §2).
  assistDisabled: boolean;
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
  /** Interactive apps only (the server's isApp rule). */
  apps?: boolean;
  limit?: number;
  cursor?: string;
}

