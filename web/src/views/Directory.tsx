import { useCallback, useEffect, useState } from "react";
import { BuildingsIcon } from "@phosphor-icons/react";
import { api, type DirectoryTenant, type JoinRequest } from "@/lib/api";
import { cn } from "@/lib/cn";

// Directory is the self-service surface for a user to find and request to join
// an organization (tenant). It lists active tenants with the caller's current
// relation (member / pending / rejected / none) and lets them apply. Approval
// is an admin action; after it, the user's next login lands in that tenant.
export function Directory() {
  const [tenants, setTenants] = useState<DirectoryTenant[] | null>(null);
  const [mine, setMine] = useState<JoinRequest[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [dir, req] = await Promise.all([api.tenantDirectory(), api.myJoinRequests()]);
      setTenants(dir.tenants ?? []);
      setMine(req.requests ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load organizations");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const apply = async (tenantId: string) => {
    setBusy(tenantId);
    setError(null);
    try {
      await api.requestJoin(tenantId);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to submit request");
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 p-4 md:p-8">
      <header className="flex items-center gap-3">
        <BuildingsIcon size={26} weight="fill" className="text-[var(--color-accent)]" />
        <div className="flex-1">
          <h1 className="text-xl font-semibold tracking-tight">Organizations</h1>
          <p className="text-sm text-[var(--color-fg-muted)]">
            Browse organizations and request to join
          </p>
        </div>
      </header>

      {error && (
        <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-4 py-3 text-sm text-[var(--color-fg-muted)]">
          {error}
        </div>
      )}

      <div className="flex flex-col gap-2">
        {tenants && tenants.length > 0 ? (
          tenants.map((t) => (
            <div
              key={t.tenantId}
              className="flex items-center gap-3 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3"
            >
              <div className="flex-1">
                <div className="font-medium">{t.name || t.tenantId}</div>
                <div className="text-xs text-[var(--color-fg-subtle)]">{t.tenantId}</div>
              </div>
              <RelationAction
                relation={t.relation}
                busy={busy === t.tenantId}
                onApply={() => void apply(t.tenantId)}
              />
            </div>
          ))
        ) : (
          <div className="rounded-[var(--radius)] border border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-fg-subtle)]">
            {tenants ? "No organizations available." : "Loading…"}
          </div>
        )}
      </div>

      {mine.length > 0 && (
        <section className="flex flex-col gap-2">
          <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
            My requests
          </h2>
          <div className="overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)]">
            <table className="w-full text-sm">
              <tbody>
                {mine.map((r) => (
                  <tr key={r.tenantId} className="border-t border-[var(--color-border)] first:border-t-0">
                    <td className="px-3 py-2">{r.tenantId}</td>
                    <td className="px-3 py-2 text-right">
                      <StatusBadge status={r.status} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </div>
  );
}

function RelationAction({
  relation,
  busy,
  onApply,
}: {
  relation: DirectoryTenant["relation"];
  busy: boolean;
  onApply: () => void;
}) {
  if (relation === "member") return <StatusBadge status="member" />;
  if (relation === "pending") return <StatusBadge status="pending" />;
  return (
    <button
      onClick={onApply}
      disabled={busy}
      className="tactile rounded-[var(--radius)] bg-[var(--color-accent)] px-4 py-1.5 text-sm font-medium text-[var(--color-accent-fg,#fff)] disabled:opacity-50"
    >
      {busy ? "Requesting…" : relation === "rejected" ? "Request again" : "Request to join"}
    </button>
  );
}

function StatusBadge({ status }: { status: string }) {
  return (
    <span
      className={cn(
        "rounded-full px-2 py-0.5 text-xs",
        status === "member" || status === "approved"
          ? "bg-[var(--color-accent)] text-[var(--color-accent-fg,#fff)]"
          : status === "rejected"
            ? "bg-[var(--color-danger,#7f1d1d)] text-white"
            : "bg-[var(--color-surface-2)] text-[var(--color-fg-muted)]",
      )}
    >
      {status}
    </span>
  );
}
