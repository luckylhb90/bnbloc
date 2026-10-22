import { useState } from "react";
import { AppWindowIcon, PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { describeStatus, shellKey } from "@/lib/accountStatus";
import { accountKind, keyOf, type VaultAccount } from "@/lib/vault";
import { desktop } from "@/lib/desktop";
import { useAccountsStatus } from "@/lib/useAccountsStatus";
import { useAuth } from "@/lib/auth";
import { errorText } from "@/lib/errors";
import { displayNameOf } from "@/lib/useDisplayName";
import { Badge } from "@/ui/Card";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Switch } from "@/ui/Segment";
import { Field, Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";

// TestBadge says an account is a test account (an API token a super admin
// issued), so no one mistakes whom they act as.
export function TestBadge() {
  return (
    <Badge tone="neutral" title="A test account: it acts with the role it was given for testing">
      Test
    </Badge>
  );
}

// AccountsDialog manages the accounts of this app: show one in this window or
// in a window of its own, remove one, sign in another, add a test account by
// its API token (docs/prd/2026-10-22-multi-account.md).
export function AccountsDialog({ open, onClose }: { readonly open: boolean; readonly onClose: () => void }) {
  const { accounts, current, switchAccount, openAccount, addAccount, addTestAccount, removeAccount } = useAuth();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const [token, setToken] = useState("");
  const [err, setErr] = useState("");
  // The account being removed (asking first, with the choice to forget its
  // data on this computer) and that choice.
  const [removing, setRemoving] = useState<VaultAccount | null>(null);
  const [deleteData, setDeleteData] = useState(false);
  // The shell's state of each account, read while the dialog is open.
  const statuses = useAccountsStatus(open);
  const gateways = new Set(accounts.map((a) => a.gateway));

  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what);
    setErr("");
    try {
      await fn();
    } catch (e) {
      setErr(errorText(e, "That did not work"));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Dialog open={open} onClose={onClose} title="Accounts" description="Every account signed in here stays signed in. Show one in this window, or open it in a window of its own to watch two side by side." size="md">
      <div className="flex flex-col gap-4">
        <ul className="flex flex-col gap-2">
          {accounts.map((a) => (
            <AccountRow
              key={keyOf(a)}
              account={a}
              shown={keyOf(a) === current}
              busy={busy === keyOf(a)}
              onSwitch={() => void run(keyOf(a), () => switchAccount(keyOf(a)))}
              onOpen={() => void run(keyOf(a), () => openAccount(keyOf(a)))}
              state={describeStatus(statuses.get(shellKey(a.gateway, a.record.subject ?? "")))}
              gateway={gateways.size > 1 ? hostOf(a.gateway) : ""}
              onRemove={() => {
                setDeleteData(false);
                setRemoving(a);
              }}
            />
          ))}
        </ul>
        {removing && (
          <div role="alertdialog" aria-label="Remove account" className="flex flex-col gap-3 rounded-[var(--radius)] border border-[var(--color-warning)]/50 p-3 text-sm">
            <p>
              Sign <span className="font-medium">{displayNameOf(removing.record.subject ?? "")}</span> out and remove it from this app?
              Every window that shows it signs out, and its sign-in on the gateway ends.
            </p>
            {desktop.available() && (
              <label className="flex items-center gap-3 text-[var(--color-fg-muted)]">
                <Switch checked={deleteData} onChange={setDeleteData} ariaLabel="Also delete this account's data on this computer" />
                Also delete its data on this computer (its settings, sign-ins to tools, sandbox disks)
              </label>
            )}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={() => setRemoving(null)}>
                Cancel
              </Button>
              <Button
                variant="danger"
                size="sm"
                disabled={busy !== null}
                onClick={() => {
                  const target = removing;
                  setRemoving(null);
                  void run(keyOf(target), () => removeAccount(keyOf(target), deleteData).then(() => void toast.info("Account removed")));
                }}
              >
                Remove
              </Button>
            </div>
          </div>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            onClick={() => {
              onClose();
              addAccount();
            }}
          >
            <PlusIcon size={14} /> Add an account
          </Button>
        </div>
        <p className="text-xs text-[var(--color-fg-subtle)]">
          Accounts are kept in this app on this computer, and what runs in the app can use any of them: add only accounts you act as.
        </p>
        <Field label="Add a test account" error={err} hint="Paste the API token of a test account (Platform → Test accounts). It signs in as that account.">
          <div className="flex gap-2">
            <Input
              placeholder="bnbloc_at_…"
              value={token}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => setToken(e.target.value)}
            />
            <Button
              variant="primary"
              disabled={!token.trim() || busy !== null}
              onClick={() => void run("token", () => addTestAccount(token, true))}
            >
              {busy === "token" ? "Adding…" : "Add and switch"}
            </Button>
          </div>
        </Field>
      </div>
    </Dialog>
  );
}

function hostOf(gateway: string): string {
  try {
    return new URL(gateway).host;
  } catch {
    return gateway;
  }
}

function AccountRow({
  account,
  shown,
  busy,
  state,
  gateway,
  onSwitch,
  onOpen,
  onRemove,
}: {
  readonly account: VaultAccount;
  readonly shown: boolean;
  readonly busy: boolean;
  /** What the shell says of it ("agents online"), or "". */
  readonly state: string;
  /** Its gateway's host, when the vault holds accounts of several. */
  readonly gateway: string;
  readonly onSwitch: () => void;
  readonly onOpen: () => void;
  readonly onRemove: () => void;
}) {
  const subject = account.record.subject ?? "";
  return (
    <li
      className={
        shown
          ? "flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] px-3 py-2 text-sm"
          : "flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-2 text-sm"
      }
    >
      <span className="min-w-0 flex-1">
        <span className="block truncate">
          {displayNameOf(subject)}
          {shown && <span className="text-[var(--color-fg-subtle)]"> · this window</span>}
        </span>
        {(gateway || state || account.signedOut) && (
          <span className="block truncate text-xs text-[var(--color-fg-subtle)]">
            {[gateway, account.signedOut ? "sign in again" : state].filter(Boolean).join(" · ")}
          </span>
        )}
      </span>
      {accountKind(account.record) === "test" && <TestBadge />}
      {!shown && (
        <Button variant="outline" size="sm" disabled={busy} onClick={onSwitch}>
          {busy ? "Switching…" : "Switch"}
        </Button>
      )}
      <Button variant="ghost" size="icon" disabled={busy} onClick={onOpen} title="Open in a new window" aria-label={`Open ${displayNameOf(subject)} in a new window`}>
        <AppWindowIcon size={15} />
      </Button>
      <Button variant="ghost" size="icon" disabled={busy} onClick={onRemove} title="Sign this account out and remove it" aria-label={`Remove ${displayNameOf(subject)}`}>
        <TrashIcon size={15} />
      </Button>
    </li>
  );
}
