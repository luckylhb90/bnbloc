import { useCallback, useEffect, useRef, useState } from "react";
import { Select } from "@/ui/Select";
import { ArrowClockwiseIcon, DownloadSimpleIcon, ScrollIcon } from "@phosphor-icons/react";
import { type PlatformTenant } from "@/lib/api";
import {
  appendAuditPage,
  AUDIT_KIND_GROUPS,
  auditOutcomeTone,
  EMPTY_AUDIT_FILTER,
  type AuditLogEntry,
  type AuditLogFilter,
  type AuditLogScope,
} from "@/lib/auditLog";
import { auditLogApi } from "@/lib/auditLogApi";
import { cn } from "@/lib/cn";
import { Button } from "@/ui/Button";
import { Skeleton } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Input } from "@/ui/Input";
import { EmptyState } from "@/ui/Page";
import { useToast } from "@/ui/Toast";
import { UserWithEmail } from "@/views/UserName";
import { useEmailMask } from "@/lib/privacyMode";
import { errorText } from "@/lib/errors";

const selectClass =
  "h-10 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-2 text-sm";

// AuditLogTab is the admin audit log (R13): every audit trail merged newest
// first, with filters, "Load more" paging and CSV export. An org admin reads
// their own tenant (the tenant the session acts in); a super-admin reads the
// platform log across tenants, optionally narrowed to one tenant.
export function AuditLogTab({
  platform,
  tenants,
}: {
  readonly platform: boolean;
  readonly tenants: readonly PlatformTenant[];
}) {
  const toast = useToast();
  const [tenantFilter, setTenantFilter] = useState("");
  const [draft, setDraft] = useState<AuditLogFilter>(EMPTY_AUDIT_FILTER);
  const [filter, setFilter] = useState<AuditLogFilter>(EMPTY_AUDIT_FILTER);
  const [entries, setEntries] = useState<AuditLogEntry[] | null>(null);
  const mask = useEmailMask();
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [error, setError] = useState("");
  // Each reload bumps the generation so a slow older response is dropped.
  const generation = useRef(0);

  const logScope: AuditLogScope = platform
    ? { kind: "platform", tenant: tenantFilter || undefined }
    : { kind: "tenant" };
  const scopeKey = JSON.stringify(logScope);

  const load = useCallback(() => {
    const gen = ++generation.current;
    setEntries(null);
    setCursor(undefined);
    setError("");
    auditLogApi
      .page(JSON.parse(scopeKey) as AuditLogScope, filter)
      .then((r) => {
        if (gen !== generation.current) return;
        setEntries([...(r.entries ?? [])]);
        setCursor(r.nextCursor);
      })
      .catch((e) => {
        if (gen !== generation.current) return;
        setEntries([]);
        setError(errorText(e, "Could not load the audit log"));
      });
  }, [scopeKey, filter]);
  useEffect(load, [load]);

  const loadMore = async () => {
    if (!cursor) return;
    const gen = generation.current;
    setLoadingMore(true);
    try {
      const r = await auditLogApi.page(logScope, filter, cursor);
      if (gen !== generation.current) return;
      setEntries((shown) => appendAuditPage(shown ?? [], r.entries ?? []));
      setCursor(r.nextCursor);
    } catch (e) {
      toast.error(errorText(e, "Could not load more events"));
    } finally {
      setLoadingMore(false);
    }
  };

  const download = async () => {
    setDownloading(true);
    try {
      const { truncated } = await auditLogApi.downloadCsv(logScope, filter);
      if (truncated > 0) toast.info(`Export capped at ${truncated} events; narrow the date range for the rest`);
    } catch (e) {
      toast.error(errorText(e, "CSV export failed"));
    } finally {
      setDownloading(false);
    }
  };

  const apply = (e: React.FormEvent) => {
    e.preventDefault();
    if (draft.from && draft.to && draft.to < draft.from) {
      toast.error("The end date is before the start date");
      return;
    }
    setFilter(draft);
  };
  const reset = () => {
    setDraft(EMPTY_AUDIT_FILTER);
    setFilter(EMPTY_AUDIT_FILTER);
  };

  return (
    <section className="flex flex-col gap-4">
      <form onSubmit={apply} className="flex flex-wrap items-end gap-2">
        {platform && (
          <Field label="Tenant">
            <Select value={tenantFilter} onChange={(e) => setTenantFilter(e.target.value)} className={selectClass}>
              <option value="">All tenants</option>
              {tenants.map((t) => (
                <option key={t.tenantId} value={t.tenantId}>
                  {t.name || t.tenantId}
                </option>
              ))}
            </Select>
          </Field>
        )}
        <Field label="Kind">
          <Select
            value={draft.kind}
            onChange={(e) => setDraft({ ...draft, kind: e.target.value })}
            className={selectClass}
          >
            {AUDIT_KIND_GROUPS.map((g) => (
              <option key={g.id} value={g.id}>
                {g.label}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Actor">
          <Input
            value={draft.actor}
            onChange={(e) => setDraft({ ...draft, actor: e.target.value })}
            placeholder="user id"
            className="w-36"
          />
        </Field>
        <Field label="Outcome">
          <Input
            value={draft.outcome}
            onChange={(e) => setDraft({ ...draft, outcome: e.target.value })}
            placeholder="e.g. deny"
            className="w-28"
          />
        </Field>
        <Field label="From">
          <Input type="date" value={draft.from} onChange={(e) => setDraft({ ...draft, from: e.target.value })} className="w-40" />
        </Field>
        <Field label="To">
          <Input type="date" value={draft.to} onChange={(e) => setDraft({ ...draft, to: e.target.value })} className="w-40" />
        </Field>
        <Button type="submit" variant="primary">
          Apply
        </Button>
        <Button type="button" variant="ghost" onClick={reset}>
          Reset
        </Button>
        <div className="ml-auto flex gap-2">
          <Button type="button" variant="outline" onClick={load} title="Refresh">
            <ArrowClockwiseIcon size={14} /> Refresh
          </Button>
          <Button type="button" variant="outline" onClick={() => void download()} disabled={downloading}>
            <DownloadSimpleIcon size={14} /> {downloading ? "Exporting…" : "Export CSV"}
          </Button>
        </div>
      </form>

      {error && <ErrorBox>{error}</ErrorBox>}

      {entries === null ? (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-9" />
          <Skeleton className="h-9" />
          <Skeleton className="h-9" />
        </div>
      ) : entries.length === 0 && !error ? (
        <EmptyState
          icon={<ScrollIcon size={28} />}
          title="No audit events"
          hint="Nothing matches these filters yet."
        />
      ) : (
        <div className="overflow-x-auto rounded-[var(--radius)] border border-[var(--color-border)]">
          <table className="w-full text-sm">
            <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
              <tr>
                <th className="px-3 py-2">When</th>
                {platform && <th className="px-3 py-2">Tenant</th>}
                <th className="px-3 py-2">Kind</th>
                <th className="px-3 py-2">Action</th>
                <th className="px-3 py-2">Subject</th>
                <th className="px-3 py-2">Actor</th>
                <th className="px-3 py-2">Outcome</th>
                <th className="px-3 py-2">Detail</th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.id} className="border-t border-[var(--color-border)] align-top">
                  <td className="whitespace-nowrap px-3 py-2 tabular-nums text-[var(--color-fg-muted)]">
                    {new Date(e.atMs).toLocaleString()}
                  </td>
                  {platform && (
                    <td className="px-3 py-2 text-[var(--color-fg-muted)]">{e.tenantId || "platform"}</td>
                  )}
                  <td className="px-3 py-2">{e.kind}</td>
                  <td className="px-3 py-2">{e.action}</td>
                  <td className="max-w-[10rem] truncate px-3 py-2" title={mask(e.name)}>
                    {mask(e.name) || "—"}
                  </td>
                  <td className="max-w-[10rem] px-3 py-2">
                    {e.actor ? <UserWithEmail id={e.actor} className="max-w-full" /> : "—"}
                  </td>
                  <td className="px-3 py-2">
                    {e.outcome ? <OutcomeBadge outcome={e.outcome} /> : "—"}
                  </td>
                  <td className="max-w-[18rem] px-3 py-2 text-[var(--color-fg-muted)]">
                    <span className="line-clamp-2 break-words" title={mask(e.detail ?? "")}>
                      {mask([e.roomId ? `room ${e.roomId}` : "", e.detail ?? ""].filter(Boolean).join(" · ")) || "—"}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {cursor && entries && entries.length > 0 && (
        <div className="flex justify-center">
          <Button variant="outline" onClick={() => void loadMore()} disabled={loadingMore}>
            {loadingMore ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </section>
  );
}

function Field({ label, children }: { readonly label: string; readonly children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs text-[var(--color-fg-subtle)]">
      {label}
      {children}
    </label>
  );
}

function OutcomeBadge({ outcome }: { readonly outcome: string }) {
  const tone = auditOutcomeTone(outcome);
  return (
    <span
      className={cn(
        "rounded-full px-2 py-0.5 text-xs",
        tone === "bad" && "bg-[var(--color-danger)]/15 text-[var(--color-danger)]",
        tone === "good" && "bg-[var(--color-surface-2)] text-[var(--color-fg)]",
        tone === "neutral" && "bg-[var(--color-surface-2)] text-[var(--color-fg-muted)]",
      )}
    >
      {outcome}
    </span>
  );
}
