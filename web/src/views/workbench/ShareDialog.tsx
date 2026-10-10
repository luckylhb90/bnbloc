import { useCallback, useEffect, useState } from "react";
import { Select } from "@/ui/Select";
import { CopyIcon, LinkIcon } from "@phosphor-icons/react";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/cn";
import { appLinkBase } from "@/lib/config";
import { relativeTime } from "@/lib/format";
import { shareLinkUrl } from "@/lib/hashRoute";
import {
  DEFAULT_SHARE_DAYS,
  expiresLabel,
  SHARE_EXPIRY_OPTIONS,
  shareRequest,
  shareState,
  shareVersionLabel,
  viewsLabel,
  type ShareView,
} from "@/lib/shares";
import { sharesApi } from "@/lib/sharesApi";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";
import { displayNameOf } from "@/lib/useDisplayName";

// ShareDialog creates and manages public links to one deliverable: pick a
// version ("this version" or "always the latest") and a lifetime, create the
// link (copied at once — its token is shown only this one time), and see
// the existing links with their views and expiry, revoking any. Opened from
// a workbench tab's toolbar and a file card's menu; both hide it when the
// organization switched public links off.

const SELECT =
  "h-9 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 text-sm text-[var(--color-fg)]";

function copy(text: string): Promise<void> {
  return navigator.clipboard ? navigator.clipboard.writeText(text) : Promise.reject(new Error("no clipboard"));
}

export function ShareDialog({
  open,
  onClose,
  roomId,
  artifactId,
  fileName,
  version,
}: {
  readonly open: boolean;
  readonly onClose: () => void;
  readonly roomId: string;
  readonly artifactId: string;
  readonly fileName: string;
  // The version the member is looking at ("This version").
  readonly version: number;
}) {
  const toast = useToast();
  const [latest, setLatest] = useState<"latest" | "this">("latest");
  const [days, setDays] = useState(DEFAULT_SHARE_DAYS);
  const [busy, setBusy] = useState(false);
  const [link, setLink] = useState<string | null>(null);
  const [shares, setShares] = useState<ShareView[] | null>(null);
  const [listFailed, setListFailed] = useState(false);

  const reload = useCallback(async () => {
    try {
      setShares((await sharesApi.list(roomId, artifactId)).shares ?? []);
      setListFailed(false);
    } catch {
      setListFailed(true);
    }
  }, [roomId, artifactId]);

  useEffect(() => {
    if (!open) return;
    setLink(null);
    setShares(null);
    void reload();
  }, [open, reload]);

  const copyLink = (url: string) =>
    copy(url).then(
      () => toast.success("Link copied"),
      () => toast.error("Could not copy the link"),
    );

  const create = async () => {
    setBusy(true);
    try {
      const res = await sharesApi.create(roomId, artifactId, shareRequest({ latest: latest === "latest", version, days }));
      const url = shareLinkUrl(appLinkBase(), res.token);
      setLink(url);
      void copyLink(url);
      void reload();
    } catch (e) {
      toast.error(e instanceof ApiError && e.message ? e.message : "Could not create the link");
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (share: ShareView) => {
    try {
      await sharesApi.revoke(roomId, artifactId, share.shareId);
      toast.success("Link revoked");
      void reload();
    } catch (e) {
      toast.error(e instanceof ApiError && e.message ? e.message : "Could not revoke the link");
    }
  };

  const now = Date.now();
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={`Share ${fileName}`}
      description="Anyone with the link can view and download this file without signing in. Nothing else of the room is shown."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Done
          </Button>
          <Button variant="primary" onClick={() => void create()} disabled={busy}>
            <LinkIcon size={16} /> {busy ? "Creating…" : "Create link"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-1.5">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Version</span>
          <Segment
            options={[
              { value: "latest", label: "Always the latest" },
              { value: "this", label: `This version (v${version})` },
            ]}
            value={latest}
            onChange={setLatest}
          />
        </div>
        <label className="flex flex-col gap-1.5">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Link expires after</span>
          <Select className={SELECT} value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {SHARE_EXPIRY_OPTIONS.map((o) => (
              <option key={o.days} value={o.days}>
                {o.label}
              </option>
            ))}
          </Select>
        </label>
        {link && (
          <div className="flex flex-col gap-1.5 rounded-[var(--radius)] border border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] p-3">
            <div className="flex items-center gap-2">
              <input
                readOnly
                value={link}
                aria-label="Share link"
                onFocus={(e) => e.currentTarget.select()}
                className="min-w-0 flex-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5 font-mono text-xs text-[var(--color-fg)]"
              />
              <Button size="sm" variant="outline" onClick={() => void copyLink(link)}>
                <CopyIcon size={14} /> Copy
              </Button>
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">This link is shown only once. Copy it now.</p>
          </div>
        )}
        <div className="flex flex-col gap-1.5">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Links</span>
          {listFailed ? (
            <p className="text-sm text-[var(--color-fg-subtle)]">Could not load the links.</p>
          ) : shares === null ? (
            <p className="text-sm text-[var(--color-fg-subtle)]">Loading…</p>
          ) : shares.length === 0 ? (
            <p className="text-sm text-[var(--color-fg-subtle)]">No links yet.</p>
          ) : (
            <ul className="flex flex-col divide-y divide-[var(--color-border)] rounded-[var(--radius)] border border-[var(--color-border)]">
              {shares.map((s) => {
                const state = shareState(s, now);
                return (
                  <li key={s.shareId} className="flex items-center gap-2 px-3 py-2">
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm text-[var(--color-fg)]">{shareVersionLabel(s.version)}</p>
                      <p className="truncate text-xs text-[var(--color-fg-subtle)]">
                        {viewsLabel(s.viewCount)} · <UserName id={s.createdBy} /> · {relativeTime(s.createdAtMs)}
                      </p>
                    </div>
                    <span
                      className={cn(
                        "shrink-0 text-xs",
                        state === "active" ? "text-[var(--color-fg-muted)]" : "text-[var(--color-fg-subtle)]",
                      )}
                    >
                      {state === "active" ? expiresLabel(s.expiresAtMs, now) : state === "revoked" ? "Revoked" : "Expired"}
                    </span>
                    {state === "active" && (
                      <Button size="sm" variant="ghost" onClick={() => void revoke(s)} aria-label={`Revoke link created by ${displayNameOf(s.createdBy)}`}>
                        Revoke
                      </Button>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </div>
    </Dialog>
  );
}
