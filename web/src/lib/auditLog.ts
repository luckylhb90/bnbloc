// Audit log (R13): one merged, newest-first view over the platform's audit
// trails — component lifecycle and deliveries, API tokens, admin alerts,
// member/tenant administration, session revocations, and borrow decisions.
// Served by shareagent: GET /v1/tenant/audit-log (an org admin's own tenant)
// and GET /v1/platform/audit-log (super-admin, every tenant or ?tenant=).

export type { AuditLogEntry, AuditLogReply } from "./wire.gen.ts";
import type { AuditLogEntry } from "./wire.gen.ts";

// The console the log is read from: an org admin's tenant, or the platform
// (all tenants, optionally narrowed to one).
export type AuditLogScope =
  | { readonly kind: "tenant" }
  | { readonly kind: "platform"; readonly tenant?: string };

export interface AuditLogFilter {
  readonly kind: string; // a group id from AUDIT_KIND_GROUPS ("" = all)
  readonly actor: string;
  readonly outcome: string;
  readonly from: string; // YYYY-MM-DD ("" = open)
  readonly to: string; // YYYY-MM-DD, inclusive day ("" = open)
}

export const EMPTY_AUDIT_FILTER: AuditLogFilter = { kind: "", actor: "", outcome: "", from: "", to: "" };

// Kind filter groups: the server filters by a comma list of record kinds.
export const AUDIT_KIND_GROUPS: readonly { readonly id: string; readonly label: string; readonly kinds: string }[] = [
  { id: "", label: "All kinds", kinds: "" },
  { id: "components", label: "Components", kinds: "core,plugin,notifier,auth-provider,webhook" },
  { id: "borrow", label: "Borrow decisions", kinds: "borrow" },
  { id: "agent_dispatch", label: "Agent dispatches", kinds: "agent_dispatch" },
  { id: "session", label: "Sessions", kinds: "session" },
  { id: "member", label: "Members", kinds: "member" },
  { id: "tenant", label: "Tenants", kinds: "tenant" },
  { id: "api-token", label: "API tokens", kinds: "api-token" },
  { id: "admin-alert", label: "Alerts", kinds: "admin-alert" },
  { id: "retention", label: "Data retention", kinds: "retention" },
];

// addDay returns the YYYY-MM-DD after day (the "to" bound is exclusive on the
// server; the filter's "to" day is inclusive for the reader).
export function addDay(day: string): string {
  const [y, m, d] = day.split("-").map(Number);
  const next = new Date(Date.UTC(y, m - 1, d + 1));
  return next.toISOString().slice(0, 10);
}

// auditLogPath builds one page's (or the CSV export's) request path.
export function auditLogPath(
  scope: AuditLogScope,
  filter: AuditLogFilter,
  opts: { readonly cursor?: string; readonly limit?: number; readonly csv?: boolean; readonly tzOffsetMin?: number } = {},
): string {
  const q = new URLSearchParams();
  const group = AUDIT_KIND_GROUPS.find((g) => g.id === filter.kind);
  if (group?.kinds) q.set("kind", group.kinds);
  if (filter.actor.trim()) q.set("actor", filter.actor.trim());
  if (filter.outcome.trim()) q.set("outcome", filter.outcome.trim());
  if (filter.from) q.set("from", filter.from);
  if (filter.to) q.set("to", addDay(filter.to));
  if ((filter.from || filter.to) && opts.tzOffsetMin !== undefined) q.set("tzOffsetMin", String(opts.tzOffsetMin));
  if (opts.limit) q.set("limit", String(opts.limit));
  if (opts.cursor) q.set("cursor", opts.cursor);
  if (opts.csv) q.set("format", "csv");
  // The platform log narrows to one tenant with a filter; the tenant log is
  // the tenant the session acts in (a super-admin's acting tenant).
  if (scope.kind === "platform" && scope.tenant) q.set("tenant", scope.tenant);
  const base = scope.kind === "platform" ? "/v1/platform/audit-log" : "/v1/tenant/audit-log";
  const qs = q.toString();
  return qs ? `${base}?${qs}` : base;
}

// auditOutcomeTone classifies an outcome for display.
export function auditOutcomeTone(outcome: string | undefined): "bad" | "good" | "neutral" {
  switch ((outcome ?? "").toLowerCase()) {
    case "deny":
    case "denied":
    case "failed":
    case "error":
    case "rejected":
      return "bad";
    case "allow":
    case "ok":
    case "delivered":
    case "approved":
      return "good";
    default:
      return "neutral";
  }
}

// appendAuditPage adds a fetched page, dropping any entry already shown (a
// reload racing a "load more" must never duplicate rows).
export function appendAuditPage(
  shown: readonly AuditLogEntry[],
  page: readonly AuditLogEntry[],
): AuditLogEntry[] {
  const seen = new Set(shown.map((e) => e.id));
  return [...shown, ...page.filter((e) => !seen.has(e.id))];
}
