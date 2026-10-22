import { useState } from "react";
import { PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { accountKind, keyOf, type Account } from "@/lib/accounts";
import { useAuth } from "@/lib/auth";
import { errorText } from "@/lib/errors";
import { displayNameOf } from "@/lib/useDisplayName";
import { Badge } from "@/ui/Card";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
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

// AccountsDialog manages the accounts of this browser: switch, remove, add
// another sign-in, add a test account by its API token
// (docs/prd/2026-10-22-multi-account.md).
export function AccountsDialog({ open, onClose }: { readonly open: boolean; readonly onClose: () => void }) {
  const { subject, kind, accounts, switchAccount, addAccount, addTestAccount, removeAccount } = useAuth();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const [token, setToken] = useState("");
  const [err, setErr] = useState("");

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
    <Dialog open={open} onClose={onClose} title="Accounts" description="Switch between the accounts signed in on this browser." size="md">
      <div className="flex flex-col gap-4">
        <ul className="flex flex-col gap-2">
          <li className="flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] px-3 py-2 text-sm">
            <span className="min-w-0 flex-1 truncate">
              {displayNameOf(subject ?? "")} <span className="text-[var(--color-fg-subtle)]">· signed in</span>
            </span>
            {kind === "test" && <TestBadge />}
          </li>
          {accounts.map((a) => (
            <AccountRow
              key={keyOf(a)}
              account={a}
              busy={busy === keyOf(a)}
              onSwitch={() => void run(keyOf(a), () => switchAccount(keyOf(a)))}
              onRemove={() => void run(keyOf(a), () => removeAccount(keyOf(a)).then(() => void toast.info("Account removed")))}
            />
          ))}
        </ul>
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

function AccountRow({
  account,
  busy,
  onSwitch,
  onRemove,
}: {
  readonly account: Account;
  readonly busy: boolean;
  readonly onSwitch: () => void;
  readonly onRemove: () => void;
}) {
  const subject = account.record.subject ?? "";
  return (
    <li className="flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-2 text-sm">
      <span className="min-w-0 flex-1 truncate">{displayNameOf(subject)}</span>
      {accountKind(account.record) === "test" && <TestBadge />}
      <Button variant="outline" size="sm" disabled={busy} onClick={onSwitch}>
        {busy ? "Switching…" : "Switch"}
      </Button>
      <Button variant="ghost" size="icon" disabled={busy} onClick={onRemove} title="Remove this account from this browser" aria-label={`Remove ${displayNameOf(subject)}`}>
        <TrashIcon size={15} />
      </Button>
    </li>
  );
}
