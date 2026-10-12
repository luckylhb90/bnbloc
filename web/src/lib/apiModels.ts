// Gateway DTOs (wire shapes) of the /v1 surface. Re-exported by api.ts, the
// client façade, so views import them from "@/lib/api".
export interface Room {
  roomId: string;
  name?: string;
  description?: string;
  visibility: "private" | "shared";
  syncMode?: "git" | "mutagen" | "none";
  role?: string;
  isOwner?: boolean;
  memberCount?: number;
  createdBy?: string;
  createdAtMs?: number;
  preview?: boolean;
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
  readonly costUsd: number;
  readonly reported: boolean;
}

export interface Member {
  userId: string;
  role: string;
  // A platform agent's host, in the room only so its bridge drives that
  // agent: not a person (lib/platformAgents.ts splitAgentHosts).
  agentHost?: boolean;
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

export interface AgentJob {
  jobId: string;
  roomId: string;
  channelId: string;
  messageSeq: number;
  requesterUserId: string;
  ownerUserId: string;
  core: string;
  provider: string;
  status: "queued" | "claimed" | "running" | "reflowing" | "replied" | "failed" | "timed_out" | "cancelled";
  failureReason?: string;
  attempt: number;
  leaseBridgeId?: string;
  leaseExpiresAtMs?: number;
  updatedAtMs?: number;
  diagnostics?: unknown;
}


// A collaboration deliverable (spec, report, code) the room iterates on.
// Server-side object: every member, including web clients with no local
// bridge, sees the same current version.
export interface Artifact {
  artifactId: string;
  roomId: string;
  channelId: string;
  kind: string;
  title: string;
  language?: string;
  currentVersion: number;
  createdBy: string;
  createdAtMs: number;
  updatedAtMs: number;
  // Binary-capable deliverables (additive; absent on text-only artifacts):
  // the file's MIME type and name (display + extension source).
  mimeType?: string;
  fileName?: string;
}

// A stored preview image for one version (cards show it when present).
export interface ArtifactThumbnail {
  mimeType: string;
  width: number;
  height: number;
}

// One immutable revision. List responses omit content; it is fetched per
// version on demand: `content` for inline text, the content route for
// blob-backed files (which never carry `content`).
export interface ArtifactVersion {
  artifactId: string;
  version: number;
  parentVersion: number;
  contentHash: string;
  content?: string;
  summary?: string;
  authorUserId: string;
  createdAtMs: number;
  // Additive file fields (absent on versions published before them).
  storage?: "inline" | "blob";
  sizeBytes?: number;
  mimeType?: string;
  // The agent job / chat message that produced this version: the explicit
  // link a chat file card anchors on.
  sourceJobId?: string;
  sourceSeq?: number;
  thumbnail?: ArtifactThumbnail;
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

export interface AgentTarget {
  id: string;
  provider: string;
  localAgentUrl?: string;
  localAgentToken?: string;
  runViaGateway?: boolean;
  // Which daemon core backend this target lives on.
  // The id is already core-qualified ("<core>/<rawid>"); this is the explicit
  // core for grouping in the UI. Empty for legacy/borrowed targets.
  core: string;
  // Model + reasoning pickers discovered by the core (empty => no picker). The
  // frontend hardcodes nothing: these mirror the agent CLI's own config. Models
  // are plain ids; reasoning is per-target (the core exposes one ladder).
  models?: string[];
  reasoningEfforts?: string[];
  // Per-model reasoning options: pick a model, get that model's efforts. Falls
  // back to reasoningEfforts when a model has no entry.
  reasoningByModel?: Record<string, string[]>;
  defaultModel?: string;
  defaultReasoning?: string;
}

export interface Core {
  name: string;
  available: boolean;
  backend?: string;
  removable?: boolean;
  // "sidecar" cores are driven by the client's own bridge (not the gateway);
  // the Agents page uses this to count a running managed sidecar as usable.
  runtime?: string;
  requiresDesktop?: boolean;
}
