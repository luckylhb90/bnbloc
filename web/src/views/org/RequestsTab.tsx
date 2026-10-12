import { useCallback, useEffect, useState } from "react";
import { api, type JoinRequest } from "@/lib/api";
import { ErrorBox } from "@/ui/ErrorBox";
import { UserWithEmail } from "@/views/UserName";
import { errorText } from "@/lib/errors";
import { listPlaceholder } from "@/ui/QueryState";
import { formatDate } from "@/lib/format";

// Org-admin: self-service join requests for the tenant (split from
// Organization.tsx).
export function RequestsTab() {
  const [requests, setRequests] = useState<JoinRequest[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const { requests } = await api.listJoinRequests("pending");
      setRequests(requests ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load requests"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const decide = async (userId: string, approve: boolean) => {
    setBusy(userId);
    setError(null);
    try {
      if (approve) await api.approveJoinRequest(userId);
      else await api.rejectJoinRequest(userId);
      await load();
    } catch (e) {
      setError(errorText(e, "failed to update request"));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section className="flex flex-col gap-2">
      {error && <ErrorBox>{error}</ErrorBox>}
      <div className="overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)]">
        <table className="w-full text-sm">
          <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
            <tr>
              <th className="px-3 py-2">User</th>
              <th className="px-3 py-2">Message</th>
              <th className="px-3 py-2">Requested</th>
              <th className="px-3 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {requests && requests.length > 0 ? (
              requests.map((r) => (
                <tr key={r.userId} className="border-t border-[var(--color-border)]">
                  <td className="px-3 py-2 font-medium">
                    <UserWithEmail id={r.userId} />
                  </td>
                  <td className="px-3 py-2 text-[var(--color-fg-muted)]">{r.message || "—"}</td>
                  <td className="px-3 py-2 tabular-nums text-[var(--color-fg-muted)]">
                    {formatDate(r.createdAtMs)}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="flex justify-end gap-2">
                      <button
                        onClick={() => void decide(r.userId, true)}
                        disabled={busy === r.userId}
                        className="tactile rounded-[var(--radius)] bg-[var(--color-accent)] px-3 py-1 text-xs font-medium text-[var(--color-accent-fg,#fff)] disabled:opacity-50"
                      >
                        Approve
                      </button>
                      <button
                        onClick={() => void decide(r.userId, false)}
                        disabled={busy === r.userId}
                        className="tactile rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-1 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] disabled:opacity-40"
                      >
                        Reject
                      </button>
                    </div>
                  </td>
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={4} className="px-3 py-8 text-center text-[var(--color-fg-subtle)]">
                  {listPlaceholder(requests, !!error, "No pending requests.")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}