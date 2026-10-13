import { useCallback, useEffect, useState } from "react";
import { ArrowClockwiseIcon } from "@phosphor-icons/react";
import { expiresLabel, shareAccessLabel, shareVersionLabel, viewsLabel, type TenantShareView } from "@/lib/shares";
import { sharesApi } from "@/lib/sharesApi";
import { ConfirmDialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";
import { errorText } from "@/lib/errors";
import { listPlaceholder } from "@/ui/QueryState";
import { exactTime } from "@/lib/format";

// Org-admin: every active share link of the tenant — artifact, who may open
// it, room, creator, expiry, views — with revoke. Switching links anyone can
// open off for the organization is the "Allow links anyone can open"
// setting (Settings tab); it stops every such link at once, this list
// revokes one.
export function PublicLinksTab() {
  const toast = useToast();
  const [shares, setShares] = useState<TenantShareView[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<TenantShareView | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setShares((await sharesApi.tenantShares()).shares ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load public links"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const revoke = async (s: TenantShareView) => {
    try {
      await sharesApi.revoke(s.roomId, s.artifactId, s.shareId);
      toast.success("Link revoked");
      await load();
    } catch (e) {
      toast.error(errorText(e, "Could not revoke the link"));
    }
  };

  const now = Date.now();
  return (
    <section className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-[var(--color-fg-muted)]">Active share links of this organization's rooms.</p>
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
              <th className="px-3 py-2">Who can open</th>
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
                  <td className="px-3 py-2 text-[var(--color-fg-muted)]">{shareAccessLabel(s.access, "the organization")}</td>
                  <td className="max-w-[10rem] truncate px-3 py-2 text-[var(--color-fg-muted)]" title={s.roomId}>
                    {s.roomId}
                  </td>
                  <td className="px-3 py-2 text-[var(--color-fg-muted)]">
                    <UserName id={s.createdBy} />
                  </td>
                  <td className="px-3 py-2 text-[var(--color-fg-muted)]" title={exactTime(s.expiresAtMs)}>
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
                <td colSpan={7} className="px-3 py-6 text-center text-[var(--color-fg-subtle)]">
                  {listPlaceholder(shares, !!error, "No active share links.")}
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
