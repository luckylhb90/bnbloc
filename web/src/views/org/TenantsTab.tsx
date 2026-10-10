import { useState } from "react";
import { api, type PlatformTenant } from "@/lib/api";
import { useQuery } from "@/lib/useQuery";
import { cn } from "@/lib/cn";
import { ErrorBox } from "@/ui/ErrorBox";

// Platform super-admin: the cross-tenant registry (split from
// Organization.tsx).
export function TenantsTab() {
  const {
    data: tenants,
    error: loadError,
    refetch,
  } = useQuery<PlatformTenant[]>("platform/tenants", async () => {
    const { tenants } = await api.listTenants();
    return tenants ?? [];
  });
  const [mutError, setMutError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [newId, setNewId] = useState("");
  const [newName, setNewName] = useState("");

  const error =
    mutError ??
    (loadError instanceof Error
      ? loadError.message
      : loadError
        ? "failed to load tenants"
        : null);

  const run = async (key: string, fn: () => Promise<unknown>, failMsg: string) => {
    setBusy(key);
    setMutError(null);
    try {
      await fn();
      refetch();
    } catch (e) {
      setMutError(e instanceof Error ? e.message : failMsg);
    } finally {
      setBusy(null);
    }
  };

  const create = async () => {
    const id = newId.trim();
    if (!id) return;
    await run("__create__", () => api.createTenant({ tenantId: id, name: newName.trim() || id }), "failed to create tenant");
    setNewId("");
    setNewName("");
  };

  return (
    <section className="flex flex-col gap-3">
      {error && <ErrorBox>{error}</ErrorBox>}

      {/* Create a new tenant. */}
      <div className="flex flex-wrap items-end gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
            Tenant id
          </span>
          <input
            value={newId}
            onChange={(e) => setNewId(e.target.value)}
            placeholder="acme"
            className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm"
          />
        </label>
        <label className="flex flex-1 flex-col gap-1">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
            Display name
          </span>
          <input
            value={newName}
            onChange={(e) => setNewName(e.target.value)}
            placeholder="Acme Corp"
            className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm"
          />
        </label>
        <button
          onClick={() => void create()}
          disabled={busy === "__create__" || !newId.trim()}
          className="tactile rounded-[var(--radius)] bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-[var(--color-accent-fg,#fff)] disabled:opacity-50"
        >
          {busy === "__create__" ? "Creating…" : "Create tenant"}
        </button>
      </div>

      <div className="overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)]">
        <table className="w-full text-sm">
          <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
            <tr>
              <th className="px-3 py-2">Tenant</th>
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2">Run quota</th>
              <th className="px-3 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {tenants && tenants.length > 0 ? (
              tenants.map((t) => {
                const suspended = t.status === "suspended";
                const isDefault = t.tenantId === "default";
                return (
                  <tr key={t.tenantId} className="border-t border-[var(--color-border)]">
                    <td className="px-3 py-2">
                      <span className="font-medium">{t.name || t.tenantId}</span>
                      <span className="ml-2 text-xs text-[var(--color-fg-subtle)]">{t.tenantId}</span>
                    </td>
                    <td className="px-3 py-2">
                      <span
                        className={cn(
                          "rounded-full px-2 py-0.5 text-xs",
                          suspended ? "bg-[var(--color-danger,#7f1d1d)] text-white" : "bg-[var(--color-surface-2)]",
                        )}
                      >
                        {t.status}
                      </span>
                    </td>
                    <td className="px-3 py-2 tabular-nums text-[var(--color-fg-muted)]">
                      {t.runQuota > 0 ? t.runQuota : "unlimited"}
                    </td>
                    <td className="px-3 py-2 text-right">
                      <button
                        onClick={() =>
                          void run(
                            t.tenantId,
                            () => api.setTenantStatus(t.tenantId, suspended ? "active" : "suspended"),
                            "failed to change status",
                          )
                        }
                        disabled={busy === t.tenantId || (isDefault && !suspended)}
                        className="tactile rounded-[var(--radius)] border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] disabled:opacity-40"
                        title={isDefault && !suspended ? "The default tenant cannot be suspended" : undefined}
                      >
                        {suspended ? "Activate" : "Suspend"}
                      </button>
                    </td>
                  </tr>
                );
              })
            ) : (
              <tr>
                <td colSpan={4} className="px-3 py-8 text-center text-[var(--color-fg-subtle)]">
                  {tenants ? "No tenants." : "Loading…"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}