import { useCallback, useEffect, useState } from "react";
import { Select } from "@/ui/Select";
import { ArrowDownIcon, ArrowUpIcon, PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { api, type PlatformTenant } from "@/lib/api";
import {
  describeChange,
  matchKind,
  matchOf,
  orgSyncApi,
  type OrgSyncMatchKind,
  type OrgSyncResult,
  type OrgSyncRole,
  type OrgSyncRule,
} from "@/lib/orgSync";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";
import { errorText } from "@/lib/errors";

// Platform super-admin: organization sync. Rules map the groups, department
// or email domain an auth provider asserts at sign-in onto a tenant
// membership (member or org_admin) and default rooms. The first matching rule
// for a tenant wins; manual memberships are never changed by sync.

interface Row {
  kind: OrgSyncMatchKind;
  value: string;
  tenant: string;
  role: OrgSyncRole;
  rooms: string;
}

const KINDS: { id: OrgSyncMatchKind; label: string }[] = [
  { id: "group", label: "Group" },
  { id: "department", label: "Department" },
  { id: "emailDomain", label: "Email domain" },
];

const input =
  "rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5 text-sm";
const button =
  "tactile rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-1.5 text-sm hover:bg-[var(--color-surface-2)] disabled:opacity-50";
const primary =
  "tactile rounded-[var(--radius)] bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-[var(--color-accent-fg,#fff)] disabled:opacity-50";

function toRow(r: OrgSyncRule): Row {
  const kind = matchKind(r.match);
  return { kind, value: r.match[kind] ?? "", tenant: r.tenant, role: r.role, rooms: (r.defaultRooms ?? []).join(", ") };
}

function toRule(r: Row): OrgSyncRule {
  const rooms = r.rooms
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  return { match: matchOf(r.kind, r.value.trim()), tenant: r.tenant, role: r.role, defaultRooms: rooms };
}

function splitList(v: string): string[] {
  return v
    .split(/[,\n]/)
    .map((s) => s.trim())
    .filter(Boolean);
}

export function OrgSyncTab() {
  const toast = useToast();
  const [rows, setRows] = useState<Row[] | null>(null);
  const [tenants, setTenants] = useState<PlatformTenant[]>([]);
  const [meta, setMeta] = useState<{ updatedAtMs?: number; updatedBy?: string }>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [set, t] = await Promise.all([orgSyncApi.rules(), api.listTenants()]);
      setRows(set.rules.map(toRow));
      setMeta({ updatedAtMs: set.updatedAtMs, updatedBy: set.updatedBy });
      setTenants(t.tenants ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load org sync rules"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const update = (i: number, patch: Partial<Row>) =>
    setRows((rs) => (rs ?? []).map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const move = (i: number, d: -1 | 1) =>
    setRows((rs) => {
      const next = [...(rs ?? [])];
      const j = i + d;
      if (j < 0 || j >= next.length) return next;
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });
  const remove = (i: number) => setRows((rs) => (rs ?? []).filter((_, j) => j !== i));
  const add = () =>
    setRows((rs) => [
      ...(rs ?? []),
      { kind: "group", value: "", tenant: tenants[0]?.tenantId ?? "", role: "member", rooms: "" },
    ]);

  const save = async () => {
    if (!rows) return;
    setBusy("save");
    setError(null);
    try {
      const set = await orgSyncApi.saveRules(rows.map(toRule));
      setRows(set.rules.map(toRow));
      setMeta({ updatedAtMs: set.updatedAtMs, updatedBy: set.updatedBy });
      toast.success("Org sync rules saved. They apply at each sign-in; run a resync to apply them now.");
    } catch (e) {
      setError(errorText(e, "failed to save rules"));
    } finally {
      setBusy(null);
    }
  };

  const resync = async () => {
    setBusy("resync");
    setError(null);
    try {
      const res = await orgSyncApi.resync();
      toast.success(`Resync checked ${res.subjects} member(s); ${res.changed.length} changed.`);
    } catch (e) {
      setError(errorText(e, "resync failed"));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section className="flex flex-col gap-4">
      <p className="text-sm text-[var(--color-fg-muted)]">
        Map the groups, department, or email domain your identity provider asserts at sign-in to a tenant
        membership and default rooms. Rules are checked in order; the first rule that matches a tenant decides
        the role, and the first matching rule overall picks the tenant a sign-in lands in. Memberships an admin
        added are never changed by sync; a sync membership is removed (and its sign-ins ended) once no rule
        matches it.
      </p>
      {error && <ErrorBox>{error}</ErrorBox>}

      <div className="overflow-x-auto rounded-[var(--radius)] border border-[var(--color-border)]">
        <table className="w-full text-sm">
          <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
            <tr>
              <th className="px-2 py-2">#</th>
              <th className="px-2 py-2">Match</th>
              <th className="px-2 py-2">Tenant</th>
              <th className="px-2 py-2">Role</th>
              <th className="px-2 py-2">Default rooms</th>
              <th className="px-2 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {rows && rows.length > 0 ? (
              rows.map((r, i) => (
                <tr key={i} className="border-t border-[var(--color-border)] align-top">
                  <td className="px-2 py-2 tabular-nums text-[var(--color-fg-subtle)]">{i + 1}</td>
                  <td className="px-2 py-2">
                    <div className="flex gap-1">
                      <Select
                        aria-label="Match on"
                        value={r.kind}
                        onChange={(e) => update(i, { kind: e.target.value as OrgSyncMatchKind })}
                        className={input}
                      >
                        {KINDS.map((k) => (
                          <option key={k.id} value={k.id}>
                            {k.label}
                          </option>
                        ))}
                      </Select>
                      <input
                        aria-label="Match value"
                        value={r.value}
                        onChange={(e) => update(i, { value: e.target.value })}
                        placeholder={r.kind === "emailDomain" ? "corp.example" : "engineering"}
                        className={`${input} w-40`}
                      />
                    </div>
                  </td>
                  <td className="px-2 py-2">
                    <Select
                      aria-label="Tenant"
                      value={r.tenant}
                      onChange={(e) => update(i, { tenant: e.target.value })}
                      className={input}
                    >
                      {!tenants.some((t) => t.tenantId === r.tenant) && <option value={r.tenant}>{r.tenant || "—"}</option>}
                      {tenants.map((t) => (
                        <option key={t.tenantId} value={t.tenantId}>
                          {t.name || t.tenantId}
                        </option>
                      ))}
                    </Select>
                  </td>
                  <td className="px-2 py-2">
                    <Select
                      aria-label="Role"
                      value={r.role}
                      onChange={(e) => update(i, { role: e.target.value as OrgSyncRole })}
                      className={input}
                    >
                      <option value="member">member</option>
                      <option value="org_admin">org_admin</option>
                    </Select>
                  </td>
                  <td className="px-2 py-2">
                    <input
                      aria-label="Default room ids"
                      value={r.rooms}
                      onChange={(e) => update(i, { rooms: e.target.value })}
                      placeholder="room ids, comma-separated"
                      className={`${input} w-48`}
                    />
                  </td>
                  <td className="whitespace-nowrap px-2 py-2 text-right">
                    <button aria-label="Move up" onClick={() => move(i, -1)} disabled={i === 0} className={button}>
                      <ArrowUpIcon size={12} />
                    </button>
                    <button
                      aria-label="Move down"
                      onClick={() => move(i, 1)}
                      disabled={i === rows.length - 1}
                      className={`${button} ml-1`}
                    >
                      <ArrowDownIcon size={12} />
                    </button>
                    <button aria-label="Remove rule" onClick={() => remove(i)} className={`${button} ml-1`}>
                      <TrashIcon size={12} />
                    </button>
                  </td>
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={6} className="px-3 py-8 text-center text-[var(--color-fg-subtle)]">
                  {rows ? "No rules: organization sync is off." : "Loading…"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <button onClick={add} disabled={!rows} className={`${button} flex items-center gap-1`}>
          <PlusIcon size={12} /> Add rule
        </button>
        <button onClick={() => void save()} disabled={!rows || busy !== null} className={primary}>
          {busy === "save" ? "Saving…" : "Save rules"}
        </button>
        <button onClick={() => void resync()} disabled={busy !== null} className={button}>
          {busy === "resync" ? "Resyncing…" : "Resync now"}
        </button>
        {meta.updatedAtMs ? (
          <span className="text-xs text-[var(--color-fg-subtle)]">
            Saved {new Date(meta.updatedAtMs).toLocaleString()}
            {meta.updatedBy ? (
              <>
                {" "}by <UserName id={meta.updatedBy} />
              </>
            ) : null}
          </span>
        ) : null}
      </div>

      <PreviewPanel />
      <DeprovisionPanel />
    </section>
  );
}

// PreviewPanel is the dry run: what a sign-in with these attributes would
// change against the saved rules.
function PreviewPanel() {
  const [email, setEmail] = useState("");
  const [groups, setGroups] = useState("");
  const [department, setDepartment] = useState("");
  const [result, setResult] = useState<OrgSyncResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      setResult(await orgSyncApi.preview({ email: email.trim(), groups: splitList(groups), department: department.trim() }));
    } catch (e) {
      setError(errorText(e, "preview failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3">
      <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
        Preview (saved rules)
      </h2>
      <div className="flex flex-wrap items-end gap-2">
        <input aria-label="Email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="user@corp.example" className={input} />
        <input
          aria-label="Groups"
          value={groups}
          onChange={(e) => setGroups(e.target.value)}
          placeholder="groups, comma-separated"
          className={`${input} flex-1`}
        />
        <input
          aria-label="Department"
          value={department}
          onChange={(e) => setDepartment(e.target.value)}
          placeholder="department"
          className={input}
        />
        <button onClick={() => void run()} disabled={busy || !email.trim()} className={button}>
          {busy ? "Checking…" : "Preview"}
        </button>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      {result && (
        <div className="text-sm">
          <p className="text-[var(--color-fg-muted)]">
            {result.tenant ? (
              <>
                Signs in to <span className="font-medium text-[var(--color-fg)]">{result.tenant}</span>.
              </>
            ) : (
              "No rule matches; the sign-in is not changed by sync."
            )}
          </p>
          {result.changes.length > 0 ? (
            <ul className="mt-1 list-disc pl-5">
              {result.changes.map((c, i) => (
                <li key={i}>{describeChange(c)}</li>
              ))}
            </ul>
          ) : (
            result.tenant && <p className="text-[var(--color-fg-subtle)]">Already in sync; nothing would change.</p>
          )}
        </div>
      )}
    </div>
  );
}

// DeprovisionPanel ends departed users' access. A directory feed calls the
// same route (`bnbloc org-sync deprovision`); sign-in alone never learns that
// a user stopped signing in.
function DeprovisionPanel() {
  const toast = useToast();
  const [ids, setIds] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const run = async () => {
    const list = splitList(ids);
    if (list.length === 0) return;
    if (!window.confirm(`Remove ${list.length} user(s) from every tenant and end their sign-ins?`)) return;
    setBusy(true);
    setError(null);
    try {
      const { results } = await orgSyncApi.deprovision(list);
      const sessions = results.reduce((n, r) => n + r.revokedSessions, 0);
      toast.success(`Deprovisioned ${results.length} user(s); ended ${sessions} sign-in(s).`);
      setIds("");
    } catch (e) {
      setError(errorText(e, "deprovision failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3">
      <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Deprovision</h2>
      <p className="text-sm text-[var(--color-fg-muted)]">
        Removes the users from every tenant (manual memberships included) and ends all their sign-ins.
      </p>
      <textarea
        aria-label="User ids"
        value={ids}
        onChange={(e) => setIds(e.target.value)}
        rows={3}
        placeholder="user ids, one per line"
        className={input}
      />
      {error && <ErrorBox>{error}</ErrorBox>}
      <div>
        <button onClick={() => void run()} disabled={busy || splitList(ids).length === 0} className={button}>
          {busy ? "Deprovisioning…" : "Deprovision"}
        </button>
      </div>
    </div>
  );
}
