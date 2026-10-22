import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { errorText } from "@/lib/errors";
import { exactTime } from "@/lib/format";
import {
  TEST_ACCOUNT_ROLES,
  createTestAccount,
  removeTestAccount,
  testAccountRows,
  testAccountSubject,
  type TestAccountRole,
  type TestAccountRow,
} from "@/lib/testAccounts";
import type { APITokenView } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { ConfirmDialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { useCopy } from "@/ui/useCopy";

// Platform super admin: test accounts (docs/prd/2026-10-22-multi-account.md
// §2.3). A test account is a svc:<name> identity with an organization role
// you choose; create one, add it to this browser, and switch to it from the
// account menu to try what that role sees. It adds no power a super admin did
// not have: it issues the account's API token and sets its role in the order
// that keeps the role right. The accounts are those of the tenant the console
// acts in.

const TTL_DAYS = [7, 30, 90] as const;
type TtlDays = (typeof TTL_DAYS)[number];

const ROLE_LABEL: Record<TestAccountRole, string> = { viewer: "Viewer", member: "Member", org_admin: "Org admin" };
const ROLE_HINT: Record<TestAccountRole, string> = { viewer: "read-only", member: "can create", org_admin: "manages org" };

interface Issued {
  readonly subject: string;
  readonly token: string;
}

export function TestAccountsTab() {
  const toast = useToast();
  const copy = useCopy();
  const { addTestAccount } = useAuth();
  const [rows, setRows] = useState<TestAccountRow[] | null>(null);
  const [tokens, setTokens] = useState<readonly APITokenView[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [role, setRole] = useState<TestAccountRole>("viewer");
  const [ttl, setTtl] = useState<TtlDays>(30);
  const [issued, setIssued] = useState<Issued | null>(null);
  const [removing, setRemoving] = useState<TestAccountRow | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [members, list] = await Promise.all([api.tenantMembers(), api.apiTokens()]);
      setTokens(list.tokens);
      setRows(testAccountRows(members.members, list.tokens));
    } catch (e) {
      setError(errorText(e, "failed to load the test accounts"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what);
    setError(null);
    try {
      await fn();
      await load();
    } catch (e) {
      setError(errorText(e, "that did not work"));
    } finally {
      setBusy(null);
    }
  };

  const subject = testAccountSubject(name);
  const create = () =>
    subject &&
    run("create", async () => {
      const made = await createTestAccount(api, subject, role, ttl);
      setIssued({ subject: made.subject, token: made.token });
      setName("");
    });
  const issueAnother = (row: TestAccountRow) =>
    run(row.subject, async () => {
      const made = await api.createApiToken({ subject: row.subject, label: `Test account: ${row.role}`, ttlDays: ttl });
      setIssued({ subject: made.subject, token: made.token });
    });
  const setRoleOf = (row: TestAccountRow, next: TestAccountRole) =>
    run(row.subject, async () => {
      await api.setTenantMemberRole(row.subject, next);
      toast.success(`${row.subject} is now ${ROLE_LABEL[next]}. Its next sign-in has that role.`);
    });

  return (
    <section className="flex flex-col gap-4">
      <p className="text-sm text-[var(--color-fg-muted)]">
        Test accounts are service identities with the organization role you pick, for trying what a viewer, a member or an
        admin sees. Create one, add it to this browser, and switch to it from the account menu. Remove them when you are done.
        They are created in the organization this console is acting in.
      </p>
      {error && <ErrorBox>{error}</ErrorBox>}

      {issued && (
        <Card className="flex flex-col gap-3 border-[var(--color-warning)]/50 p-4">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
            API token for {issued.subject}: copy it now, it is not shown again
          </span>
          <code className="break-all font-mono text-xs">{issued.token}</code>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={() => void copy(issued.token, "token")}>
              Copy
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => void run("add", () => addTestAccount(issued.token, false).then(() => void toast.success("Added to the accounts signed in here")))}
            >
              Add to my accounts
            </Button>
            <Button size="sm" variant="primary" onClick={() => void run("switch", () => addTestAccount(issued.token, true))}>
              Add and switch
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setIssued(null)}>
              Done
            </Button>
          </div>
        </Card>
      )}

      <Card className="flex flex-col gap-4 p-4">
        <h3 className="text-sm font-semibold">New test account</h3>
        <Field label="Name" hint={subject ? `Signs in as ${subject}` : "Lowercase letters, digits and . _ - (for example qa-viewer)"}>
          <Input placeholder="qa-viewer" value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
        </Field>
        <Field label="Role">
          <Segment
            value={role}
            onChange={setRole}
            options={TEST_ACCOUNT_ROLES.map((r) => ({ value: r, label: ROLE_LABEL[r], hint: ROLE_HINT[r] }))}
          />
        </Field>
        <Field label="Token valid for">
          <Segment value={String(ttl) as `${TtlDays}`} onChange={(v) => setTtl(Number(v) as TtlDays)} options={TTL_DAYS.map((d) => ({ value: String(d) as `${TtlDays}`, label: `${d} days` }))} />
        </Field>
        <div>
          <Button variant="primary" disabled={!subject || busy !== null} onClick={() => void create()}>
            {busy === "create" ? "Creating…" : "Create test account"}
          </Button>
        </div>
      </Card>

      <Card className="p-0">
        {rows === null ? (
          <p className="p-4 text-sm text-[var(--color-fg-muted)]">Loading…</p>
        ) : rows.length === 0 ? (
          <p className="p-4 text-sm text-[var(--color-fg-muted)]">No test accounts in this organization yet.</p>
        ) : (
          <ul className="divide-y divide-[var(--color-border)]">
            {rows.map((row) => (
              <li key={row.subject} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium">{row.subject}</span>
                  <span className="block text-xs text-[var(--color-fg-subtle)]">
                    {row.activeTokens === 0 ? "no active token" : `${row.activeTokens} active token${row.activeTokens === 1 ? "" : "s"}`}
                    {row.lastUsedAtMs > 0 && ` · last used ${exactTime(row.lastUsedAtMs)}`}
                  </span>
                </span>
                <Segment
                  size="sm"
                  value={row.role as TestAccountRole}
                  onChange={(next) => void setRoleOf(row, next)}
                  options={TEST_ACCOUNT_ROLES.map((r) => ({ value: r, label: ROLE_LABEL[r] }))}
                />
                <Button size="sm" variant="outline" disabled={busy !== null} onClick={() => void issueAnother(row)}>
                  New token
                </Button>
                <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => setRemoving(row)}>
                  Remove
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <ConfirmDialog
        open={removing !== null}
        onClose={() => setRemoving(null)}
        title={`Remove ${removing?.subject ?? "test account"}?`}
        description="Its tokens are revoked and it leaves the organization. A browser that holds it can no longer sign in as it."
        confirmLabel="Remove"
        danger
        onConfirm={async () => {
          const row = removing;
          setRemoving(null);
          if (row) await run(row.subject, () => removeTestAccount(api, row, tokens));
        }}
      />
    </section>
  );
}
