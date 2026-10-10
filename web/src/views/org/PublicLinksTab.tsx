import { useCallback, useEffect, useState } from "react";
import { ArrowClockwiseIcon } from "@phosphor-icons/react";
import { expiresLabel, shareVersionLabel, viewsLabel, type TenantShareView } from "@/lib/shares";
import { sharesApi } from "@/lib/sharesApi";
import { ConfirmDialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";

// Org-admin: every active public share link of the tenant — room, artifact,
// creator, expiry, views — with revoke. Switching public links off for the
// organization is the "Allow public share links" setting (Settings tab); it
// kills every link at once, this list revokes one.
export function PublicLinksTab({ scope }: { scope?: string }) {
  const toast = useToast();
  const [shares, setShares] = useState<TenantShareView[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<TenantShareView | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setShares((await sharesApi.tenantShares(scope)).shares ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load public links");
    }
  }, [scope]);

  useEffect(() => {
    void load();
  }, [load]);

  const revoke = async (s: TenantShareView) => {
    try {
      await sharesApi.revoke(s.roomId, s.artifactId, s.shareId);
      toast.success("Link revoked");
      await load();
    } catch (e) {
      toast.error(e instanceof Error && e.message ? e.message : "Could not revoke the link");
    }
  };

  const now = Date.now();
  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-[var(--color-fg-muted)]">Active links anyone can open without signing in.</p>
        <button
          type="button"
          onClick={() => void load()}
          className="tactile inline-flex items-center gap-1 rounded-[var(--radius)] px-2 py-1 text-sm text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
        >
          <ArrowClockwiseIcon size={14} /> Refresh
        </button>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      <div className="overflow-x-auto rounded-[var(--radius)] border border-[var(--color-border)]">
        <table className="w-full min-w-[40rem] text-sm">
          <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
            <tr>
              <th className="px-3 py-2">Artifact</th>
              <th className="px-3 py-2">Room</th>
              <th className="px-3 py-2">Created by</th>
              <th className="px-3 py-2">Expires</th>
              <th className="px-3 py-2">Views</th>
              <th className="px-3 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {shares && shares.length > 0 ? (
              shares.map((s) => (
                <tr key={s.shareId} className="border-t border-[var(--color-border)]">
                  <td className="max-w-[16rem] px-3 py-2">
                    <span className="block truncate font-medium" title={s.artifactTitle}>
                      {s.artifactTitle || s.artifactId}
                    </span>
                    <span className="text-xs text-[var(--color-fg-subtle)]">{shareVersionLabel(s.version)}</span>
                  </td>
                  <td className="max-w-[10rem] truncate px-3 py-2 text-[var(--color-fg-muted)]" title={s.roomId}>
                    {s.roomId}
                  </td>
                  <td className="px-3 py-2 text-[var(--color-fg-muted)]">
                    <UserName id={s.createdBy} />
                  </td>
                  <td className="px-3 py-2 text-[var(--color-fg-muted)]" title={new Date(s.expiresAtMs).toLocaleString()}>
                    {expiresLabel(s.expiresAtMs, now)}
                  </td>
                  <td className="px-3 py-2 tabular-nums text-[var(--color-fg-muted)]">{viewsLabel(s.viewCount)}</td>
                  <td className="px-3 py-2 text-right">
                    <button
                      type="button"
                      onClick={() => setConfirm(s)}
                      className="tactile rounded-[var(--radius)] px-2 py-1 text-xs text-[var(--color-danger)] hover:bg-[var(--color-surface-2)]"
                    >
                      Revoke
                    </button>
                  </td>
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={6} className="px-3 py-6 text-center text-[var(--color-fg-subtle)]">
                  {shares ? "No active public links." : "Loading…"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <ConfirmDialog
        open={confirm !== null}
        onClose={() => setConfirm(null)}
        onConfirm={() => confirm && void revoke(confirm)}
        title="Revoke this link?"
        description="Anyone opening it will see that the link is no longer available. This cannot be undone."
        confirmLabel="Revoke link"
        danger
      />
    </section>
  );
}
