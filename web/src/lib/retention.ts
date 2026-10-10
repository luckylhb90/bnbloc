// Data retention (docs/prd/2026-10-10-enterprise-scale.md §3). A super admin
// sets the platform default (GET/PUT /v1/platform/retention); an org admin
// may override it for their organization (GET/PUT /v1/tenant/retention).
// Each kind is kept for a number of days, or forever.
//
// What null means depends on the layer: in the platform default, null keeps
// forever; in a tenant override, null uses the platform default and 0 keeps
// forever. The server validates too; the accepted range comes from the
// server's `limits`, never from this file.

export type RetentionKind = "messages" | "artifactVersions" | "audit" | "agentJobs";

export interface RetentionPolicy {
  messagesDays: number | null;
  artifactVersionsDays: number | null;
  auditDays: number | null;
  agentJobsDays: number | null;
}

export interface RetentionLimits {
  minDays: number;
  maxDays: number;
}

export interface RetentionStatus {
  lastRunAtMs: number;
  lastBlobGcAtMs: number;
  lastDeleted: Record<string, number> | null;
}

export interface PlatformRetentionView {
  default: RetentionPolicy;
  limits: RetentionLimits;
  status: RetentionStatus;
}

export interface TenantRetentionView {
  tenantId: string;
  override: RetentionPolicy;
  default: RetentionPolicy;
  effective: RetentionPolicy;
  limits: RetentionLimits;
}

export type RetentionLayer = "platform" | "tenant";

const FIELD: Record<RetentionKind, keyof RetentionPolicy> = {
  messages: "messagesDays",
  artifactVersions: "artifactVersionsDays",
  audit: "auditDays",
  agentJobs: "agentJobsDays",
};

// What each kind deletes, in the words the settings show.
export const RETENTION_KINDS: readonly { readonly kind: RetentionKind; readonly label: string; readonly explains: string }[] = [
  {
    kind: "messages",
    label: "Room messages",
    explains:
      "Chat messages and agent replies in every room and channel, with their tasks and the files attached to chat. If a room has been quiet for the whole period, its last message is emptied instead of removed.",
  },
  {
    kind: "artifactVersions",
    label: "Older deliverable versions",
    explains:
      "Every version of a deliverable except the latest, which is always kept. Public links pinned to a deleted version stop working.",
  },
  {
    kind: "audit",
    label: "Audit log",
    explains: "Audit log entries: borrow decisions, component, member and organization changes, and sign-in revocations.",
  },
  {
    kind: "agentJobs",
    label: "Finished agent runs",
    explains: "Records of agent runs that replied, failed, timed out or were cancelled. Queued and running jobs are never deleted.",
  },
];

export function daysOf(p: RetentionPolicy, kind: RetentionKind): number | null {
  return p[FIELD[kind]];
}

// The editable state of one kind: use the platform default (tenant only),
// keep forever, or delete after a number of days (kept as typed text).
export type RetentionMode = "default" | "forever" | "days";
export interface RetentionField {
  mode: RetentionMode;
  days: string;
}
export type RetentionFields = Record<RetentionKind, RetentionField>;

export function fieldsFromPolicy(p: RetentionPolicy, layer: RetentionLayer): RetentionFields {
  const out = {} as RetentionFields;
  for (const { kind } of RETENTION_KINDS) {
    const d = daysOf(p, kind);
    if (d === null) out[kind] = { mode: layer === "tenant" ? "default" : "forever", days: "" };
    else if (d === 0) out[kind] = { mode: "forever", days: "" };
    else out[kind] = { mode: "days", days: String(d) };
  }
  return out;
}

// policyFromFields turns the form into the PUT body, or names the first
// invalid field. Keep forever is null in the platform default, 0 in a tenant
// override; "default" exists only in a tenant override.
export function policyFromFields(
  fields: RetentionFields,
  layer: RetentionLayer,
  limits: RetentionLimits,
): { policy: RetentionPolicy } | { error: string } {
  const policy: RetentionPolicy = { messagesDays: null, artifactVersionsDays: null, auditDays: null, agentJobsDays: null };
  for (const { kind, label } of RETENTION_KINDS) {
    const f = fields[kind];
    let value: number | null = null;
    if (f.mode === "forever") value = layer === "tenant" ? 0 : null;
    else if (f.mode === "days") {
      const n = Number(f.days.trim());
      if (!f.days.trim() || !Number.isInteger(n) || n < limits.minDays || n > limits.maxDays) {
        return { error: `${label}: enter a whole number of days between ${limits.minDays} and ${limits.maxDays}.` };
      }
      value = n;
    } else if (layer === "platform") {
      return { error: `${label}: choose keep forever or a number of days.` };
    }
    policy[FIELD[kind]] = value;
  }
  return { policy };
}

export function samePolicy(a: RetentionPolicy, b: RetentionPolicy): boolean {
  return RETENTION_KINDS.every(({ kind }) => daysOf(a, kind) === daysOf(b, kind));
}

// describeDays renders an effective value (null = keep forever).
export function describeDays(days: number | null): string {
  if (days === null || days <= 0) return "Kept forever";
  return `Deleted ${days} ${days === 1 ? "day" : "days"} after it was created`;
}

// describeEffective says what applies to an organization and why.
export function describeEffective(view: TenantRetentionView, kind: RetentionKind): string {
  const own = daysOf(view.override, kind);
  const source = own === null ? "platform default" : "your setting";
  return `${describeDays(daysOf(view.effective, kind))} (${source}).`;
}

// The worker's deletion counters, in plain words.
const ITEM_LABELS: Record<string, string> = {
  room_messages: "messages",
  room_messages_redacted: "emptied last messages",
  message_tasks: "message tasks",
  agent_messages: "agent reply records",
  attachments: "attachments",
  artifact_versions: "deliverable versions",
  artifact_shares: "public links",
  shared_agent_audit: "borrow audit entries",
  extension_audit: "audit entries",
  agent_jobs: "agent runs",
  blobs: "stored files",
};

// describeStatus summarizes the last cleanup for the platform console.
export function describeStatus(status: RetentionStatus, now: number = Date.now()): string {
  if (!status.lastRunAtMs) return "The cleanup has not run yet.";
  const parts = Object.entries(status.lastDeleted ?? {})
    .filter(([, n]) => n > 0)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([item, n]) => `${n.toLocaleString("en-US")} ${ITEM_LABELS[item] ?? item}`);
  const when = ago(status.lastRunAtMs, now);
  return parts.length > 0
    ? `The last cleanup ran ${when} and removed ${parts.join(", ")}.`
    : `The last cleanup ran ${when} and found nothing to remove.`;
}

function ago(ms: number, now: number): string {
  const min = Math.max(0, Math.round((now - ms) / 60000));
  if (min < 1) return "just now";
  if (min < 60) return `${min} ${min === 1 ? "minute" : "minutes"} ago`;
  const h = Math.round(min / 60);
  if (h < 48) return `${h} ${h === 1 ? "hour" : "hours"} ago`;
  return `on ${new Date(ms).toLocaleDateString()}`;
}
