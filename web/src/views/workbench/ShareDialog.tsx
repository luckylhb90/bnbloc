import { useCallback, useEffect, useState } from "react";
import { Select } from "@/ui/Select";
import { CopyIcon, LinkIcon, WarningIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { appLinkBase } from "@/lib/config";
import { relativeTime } from "@/lib/format";
import { shareLinkUrl } from "@/lib/hashRoute";
import {
  allowedShareAccess,
  chosenShareAccess,
  DEFAULT_SHARE_ACCESS,
  DEFAULT_SHARE_DAYS,
  expiresLabel,
  SHARE_EXPIRY_OPTIONS,
  shareAccessChanged,
  shareAccessLabel,
  shareAccessOptions,
  shareRequest,
  shareState,
  shareTargetDescription,
  shareVersionLabel,
  viewsLabel,
  type ShareAccess,
  type ShareTarget,
  type ShareView,
} from "@/lib/shares";
import { sharesApi } from "@/lib/sharesApi";
import { useTenantPolicy } from "@/lib/tenantPolicy";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { useCopy } from "@/ui/useCopy";
import { UserName } from "@/views/UserName";
import { displayNameOf } from "@/lib/useDisplayName";
import { errorText } from "@/lib/errors";

// ShareDialog creates and manages share links to one deliverable: pick who
// may open it (the organization's signed-in members, the default; anyone
// signed in, of any organization; or anyone with the link, no sign-in —
// each unless the organization switched it off), a version
// ("this version" or "always the latest") and a lifetime, create the link
// (copied at once — its token is shown only this one time), and see the
// existing links with who may open them, their views and expiry, changing
// who may open one or revoking it. Opened from a workbench tab's toolbar
// and a file card's menu.

const SELECT =
  "h-9 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 text-sm text-[var(--color-fg)]";

export function ShareDialog({
  open,
  onClose,
  roomId,
  artifactId,
  fileName,
  version,
  kind = "file",
}: {
  readonly open: boolean;
  readonly onClose: () => void;
  readonly roomId: string;
  readonly artifactId: string;
  readonly fileName: string;
  // The version the member is looking at ("This version").
  readonly version: number;
  // What the link shares (the dialog's description).
  readonly kind?: ShareTarget;
}) {
  const toast = useToast();
  const policy = useTenantPolicy();
  const org = policy?.name ?? "";
  // Until the policy is known, only a link for the organization is offered.
  const allowed = allowedShareAccess(policy);
  const accessOptions = shareAccessOptions(org, allowed);
  const [access, setAccess] = useState<ShareAccess>(DEFAULT_SHARE_ACCESS);
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
    setAccess(DEFAULT_SHARE_ACCESS);
    void reload();
  }, [open, reload]);
  // The chosen access stays one the organization allows.
  const chosen = chosenShareAccess(access, allowed);

  const copy = useCopy();
  const copyLink = (url: string) => copy(url, "link");

  const create = async () => {
    setBusy(true);
    try {
      const res = await sharesApi.create(roomId, artifactId, shareRequest({ latest: latest === "latest", version, days, access: chosen }));
      const url = shareLinkUrl(appLinkBase(), res.token);
      setLink(url);
      void copyLink(url);
      void reload();
    } catch (e) {
      toast.error(errorText(e, "Could not create the link"));
    } finally {
      setBusy(false);
    }
  };

  const changeAccess = async (share: ShareView, to: ShareAccess) => {
    try {
      await sharesApi.setAccess(roomId, artifactId, share.shareId, to);
      toast.success(shareAccessChanged(to));
      void reload();
    } catch (e) {
      toast.error(errorText(e, "Could not change who can open the link"));
    }
  };

  const revoke = async (share: ShareView) => {
    try {
      await sharesApi.revoke(roomId, artifactId, share.shareId);
      toast.success("Link revoked");
      void reload();
    } catch (e) {
      toast.error(errorText(e, "Could not revoke the link"));
    }
  };

  const now = Date.now();
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={`Share ${fileName}`}
      description={shareTargetDescription(kind)}
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
          <span id="share-access-label" className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
            Who can open it
          </span>
          <div role="radiogroup" aria-labelledby="share-access-label" className="flex flex-col gap-1.5">
            {accessOptions.map((o) => {
              const selected = chosen === o.value;
              return (
                <button
                  key={o.value}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  disabled={o.disabled}
                  onClick={() => setAccess(o.value)}
                  className={cn(
                    "tactile flex flex-col items-start gap-0.5 rounded-[var(--radius)] border px-3 py-2 text-left disabled:cursor-not-allowed disabled:opacity-60",
                    selected
                      ? "border-[var(--color-accent)] bg-[var(--color-accent-soft)]"
                      : "border-[var(--color-border)] bg-[var(--color-surface)] hover:bg-[var(--color-surface-2)]",
                  )}
                >
                  <span className="text-sm font-medium text-[var(--color-fg)]">{o.label}</span>
                  <span className="text-xs text-[var(--color-fg-muted)]">{o.hint}</span>
                </button>
              );
            })}
          </div>
          {chosen === "anyone" && (
            <p role="note" className="flex items-start gap-1.5 text-xs text-[var(--color-warning)]">
              <WarningIcon size={14} className="mt-px shrink-0" aria-hidden />
              No sign-in: whoever has this link can open it, and it can be forwarded to anyone.
            </p>
          )}
        </div>
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
            <p role="alert" className="flex flex-wrap items-center gap-2 text-sm text-[var(--color-fg-subtle)]">
              Could not load the links.
              <Button size="sm" variant="ghost" onClick={() => void reload()}>
                Retry
              </Button>
            </p>
          ) : shares === null ? (
            <p role="status" className="text-sm text-[var(--color-fg-subtle)]">
              Loading…
            </p>
          ) : shares.length === 0 ? (
            <p className="text-sm text-[var(--color-fg-subtle)]">No links yet.</p>
          ) : (
            <ul className="flex flex-col divide-y divide-[var(--color-border)] rounded-[var(--radius)] border border-[var(--color-border)]">
              {shares.map((s) => {
                const state = shareState(s, now);
                return (
                  <li key={s.shareId} className="flex items-center gap-2 px-3 py-2">
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-sm text-[var(--color-fg)]">
                        {shareVersionLabel(s.version)}
                        {/* An active link's access is its selector. */}
                        {state !== "active" && ` · ${shareAccessLabel(s.access, org)}`}
                      </p>
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
                      <Select
                        className="h-8 max-w-[11rem] shrink-0 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-1.5 text-xs text-[var(--color-fg)]"
                        value={s.access}
                        aria-label="Who can open this link"
                        onChange={(e) => {
                          const to = e.target.value as ShareAccess;
                          if (to !== s.access) void changeAccess(s, to);
                        }}
                      >
                        {accessOptions.map((o) => (
                          <option key={o.value} value={o.value} disabled={o.disabled && o.value !== s.access}>
                            {shareAccessLabel(o.value, org)}
                          </option>
                        ))}
                      </Select>
                    )}
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
