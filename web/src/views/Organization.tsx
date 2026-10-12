import { useCallback, useEffect, useState } from "react";
import { Select } from "@/ui/Select";
import { BuildingsIcon, ArrowClockwiseIcon } from "@phosphor-icons/react";
import {
  api,
  type TenantAuditRecord,
  type TenantUsage,
  type TenantMember,
  type TenantSettings,
} from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { useActingTenant } from "@/lib/session";
import { useTenants } from "@/lib/queries";
import { cn } from "@/lib/cn";
import { PlatformComponents } from "@/views/PlatformComponents";
import { PlatformReleases } from "@/views/PlatformReleases";
import { PlatformUpdateConfig } from "@/views/PlatformUpdateConfig";
import { ErrorBox } from "@/ui/ErrorBox";
import { Switch } from "@/ui/Segment";
import { RequestsTab } from "@/views/org/RequestsTab";
import { TenantsTab } from "@/views/org/TenantsTab";
import { OrgSyncTab } from "@/views/org/OrgSyncTab";
import { DeploymentCard } from "@/views/org/DeploymentCard";
import { PlatformAgentsCard } from "@/views/org/PlatformAgentsCard";
import { PlatformRetentionCard, TenantRetentionSection } from "@/views/org/RetentionCard";
import { AuditLogTab } from "@/views/org/AuditLogTab";
import { PublicLinksTab } from "@/views/org/PublicLinksTab";
import { ToolConnectorsTab } from "@/views/org/ToolConnectorsTab";
import { ContentReviewTab } from "@/views/org/ContentReviewTab";
import { PlatformContentTab } from "@/views/org/PlatformContentTab";
import { invalidateTenantPolicy } from "@/lib/tenantPolicy";
import { UserWithEmail } from "@/views/UserName";
import { can } from "@/lib/permissions";
import { errorText } from "@/lib/errors";
import { ConfirmDialog } from "@/ui/Dialog";
import { displayNameOf } from "@/lib/useDisplayName";

type Tab =
  | "overview"
  | "members"
  | "requests"
  | "content"
  | "quota"
  | "settings"
  | "links"
  | "tools"
  | "audit"
  | "tenants"
  | "orgsync"
  | "components"
  | "platformContent"
  | "updates"
  | "releases";

const TABS: { id: Tab; label: string; superOnly?: boolean }[] = [
  { id: "overview", label: "Overview" },
  { id: "members", label: "Members" },
  { id: "requests", label: "Requests" },
  { id: "content", label: "Content" },
  { id: "quota", label: "Quota" },
  { id: "settings", label: "Settings" },
  { id: "links", label: "Public links" },
  { id: "tools", label: "Tool connectors" },
  { id: "audit", label: "Audit log" },
  { id: "tenants", label: "Tenants", superOnly: true },
  { id: "orgsync", label: "Org sync", superOnly: true },
  { id: "components", label: "Components", superOnly: true },
  { id: "platformContent", label: "Platform content", superOnly: true },
  { id: "updates", label: "Updates", superOnly: true },
  { id: "releases", label: "Releases", superOnly: true },
];

// Organization is the org-admin console: tenant identity, aggregate agent usage
// against org ceilings, org-wide audit trail, member role management, quota
// editing, and tenant settings — the frontend for the tenant APIs
// (/v1/tenant/*). It is only reachable by an org_admin (the Shell hides the nav
// entry otherwise); the gateway also enforces PermManageOrg on these endpoints,
export function Organization() {
  const { tenant, role, subject } = useAuth();
  const [tab, setTab] = useState<Tab>("overview");
  const isSuper = can(role, "manage_platform");
  // The tenant this console administers is the one the session acts in: a
  // super-admin's acting tenant (lib/session.ts, sent on every request), else
  // the session's own. The picker below switches the same acting tenant.
  const { acting, switchTo } = useActingTenant(isSuper);
  const tenants = useTenants(isSuper).data ?? [];
  const activeTenant = acting ?? tenant ?? undefined;

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-6 p-4 md:p-8">
      <header className="flex items-center gap-3">
        <BuildingsIcon size={26} weight="fill" className="text-[var(--color-accent)]" />
        <div className="flex-1">
          <h1 className="text-xl font-semibold tracking-tight">
            {isSuper ? "Platform" : "Organization"}
          </h1>
          <p className="text-sm text-[var(--color-fg-muted)]">
            {activeTenant ? (
              <>
                Tenant <span className="font-medium text-[var(--color-fg)]">{activeTenant}</span>
                {role && (
                  <>
                    {" · "}
                    <span className="rounded-full bg-[var(--color-surface-2)] px-2 py-0.5 text-xs">
                      {role}
                    </span>
                  </>
                )}
              </>
            ) : (
              "No organization"
            )}
          </p>
        </div>
        {/* Super-admin only: switch which tenant this console administers. */}
        {isSuper && tenants.length > 0 && (
          <label className="flex items-center gap-2 text-sm">
            <span className="text-[var(--color-fg-subtle)]">Tenant</span>
            <Select
              value={acting ?? ""}
              onChange={(e) => switchTo(e.target.value || null)}
              className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5 text-sm"
            >
              <option value="">(home: {tenant})</option>
              {tenants.map((t) => (
                <option key={t.tenantId} value={t.tenantId}>
                  {t.name || t.tenantId} ({t.tenantId})
                </option>
              ))}
            </Select>
          </label>
        )}
      </header>

      <nav className="flex gap-1 overflow-x-auto border-b border-[var(--color-border)]">
        {TABS.filter((t) => !t.superOnly || isSuper).map((t) => (
          <button
            key={t.id}
            onClick={() => setTab(t.id)}
            className={cn(
              "tactile -mb-px border-b-2 px-3 py-2 text-sm",
              tab === t.id
                ? "border-[var(--color-accent)] text-[var(--color-fg)]"
                : "border-transparent text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
            )}
          >
            {t.label}
          </button>
        ))}
      </nav>

      {tab === "overview" && <OverviewTab key={activeTenant} role={role} />}
      {tab === "members" && <MembersTab key={activeTenant} selfId={subject} />}
      {tab === "requests" && <RequestsTab key={activeTenant} />}
      {tab === "quota" && <QuotaTab key={activeTenant} />}
      {tab === "settings" && <SettingsTab key={activeTenant} />}
      {tab === "links" && <PublicLinksTab key={activeTenant} />}
      {tab === "tools" && <ToolConnectorsTab key={activeTenant} />}
      {tab === "content" && <ContentReviewTab key={activeTenant} />}
      {tab === "audit" && (
        <AuditLogTab key={isSuper ? "platform" : activeTenant} platform={isSuper} tenants={tenants} />
      )}
      {tab === "tenants" && isSuper && <TenantsTab />}
      {tab === "orgsync" && isSuper && <OrgSyncTab />}
      {tab === "updates" && isSuper && <PlatformUpdateConfig />}
      {tab === "components" && isSuper && <PlatformComponents />}
      {tab === "platformContent" && isSuper && <PlatformContentTab />}
      {tab === "releases" && isSuper && <PlatformReleases />}
    </div>
  );
}

function OverviewTab({ role }: { role: string | null }) {
  const [usage, setUsage] = useState<TenantUsage | null>(null);
  const [records, setRecords] = useState<TenantAuditRecord[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [u, a] = await Promise.all([api.tenantUsage(), api.tenantAudit(200)]);
      setUsage(u);
      setRecords(a.records ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load organization data"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <>
      <div className="flex justify-end">
        <button
          onClick={() => void load()}
          className="tactile flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-1.5 text-sm hover:bg-[var(--color-surface-2)]"
        >
          <ArrowClockwiseIcon size={14} /> Refresh
        </button>
      </div>

      {error && <ErrorBox>{error}</ErrorBox>}

      {/* Usage against org ceilings (0 = unlimited). */}
      <section className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat label="Agent runs" value={usage ? String(usage.runsUsed) : "—"} sub={ceil(usage?.runQuota)} />
        <Stat
          label="Cost"
          value={usage ? usage.costUsed.toFixed(2) : "—"}
          sub={usage?.costAllowance ? usage.costAllowance.toFixed(2) : "unlimited"}
        />
        <Stat label="Audit events" value={records ? String(records.length) : "—"} />
        <Stat label="Role" value={role || "—"} />
      </section>

      {can(role, "manage_platform") && <DeploymentCard />}
      {can(role, "manage_platform") && <PlatformAgentsCard />}
      {can(role, "manage_platform") && <PlatformRetentionCard />}

      {/* Org-wide audit trail. */}
      <section className="flex flex-col gap-2">
        <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
          Audit trail
        </h2>
        <div className="overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)]">
          <table className="w-full text-sm">
            <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
              <tr>
                <th className="px-3 py-2">When</th>
                <th className="px-3 py-2">Actor</th>
                <th className="px-3 py-2">Action</th>
                <th className="px-3 py-2">Decision</th>
                <th className="px-3 py-2">Room</th>
              </tr>
            </thead>
            <tbody>
              {records && records.length > 0 ? (
                records.map((r, i) => (
                  <tr key={i} className="border-t border-[var(--color-border)]">
                    <td className="px-3 py-2 tabular-nums text-[var(--color-fg-muted)]">
                      {new Date(r.atMs).toLocaleString()}
                    </td>
                    <td className="px-3 py-2">
                      <UserWithEmail id={r.actorUserId} />
                    </td>
                    <td className="px-3 py-2">{r.action}</td>
                    <td className="px-3 py-2">
                      <span
                        className={cn(
                          "rounded-full px-2 py-0.5 text-xs",
                          r.decision === "deny"
                            ? "bg-[var(--color-danger,#7f1d1d)] text-white"
                            : "bg-[var(--color-surface-2)]",
                        )}
                        title={r.denyReason}
                      >
                        {r.decision}
                        {r.denyReason ? ` · ${r.denyReason}` : ""}
                      </span>
                    </td>
                    <td className="px-3 py-2 text-[var(--color-fg-subtle)]">{r.roomId.slice(0, 8)}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={5} className="px-3 py-8 text-center text-[var(--color-fg-subtle)]">
                    {loading ? "Loading…" : "No audit events yet."}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </>
  );
}

const ROLES: TenantMember["role"][] = ["org_admin", "member", "viewer"];

function MembersTab({ selfId }: { selfId: string | null }) {
  const [members, setMembers] = useState<TenantMember[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [newId, setNewId] = useState("");
  const [newRole, setNewRole] = useState<TenantMember["role"]>("member");
  const [removing, setRemoving] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const { members } = await api.tenantMembers();
      setMembers(members ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load members"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  // run reports whether fn succeeded, so a failed add keeps what was typed.
  const run = async (key: string, fn: () => Promise<unknown>, failMsg: string): Promise<boolean> => {
    setBusy(key);
    setError(null);
    try {
      await fn();
      await load();
      return true;
    } catch (e) {
      setError(errorText(e, failMsg));
      return false;
    } finally {
      setBusy(null);
    }
  };

  const addMember = async () => {
    const id = newId.trim();
    if (!id) return;
    if (await run("__add__", () => api.addTenantMember(id, newRole), "failed to add member")) setNewId("");
  };

  return (
    <section className="flex flex-col gap-3">
      {error && <ErrorBox>{error}</ErrorBox>}

      {/* Admin-driven add: seed a member before their first sign-in. */}
      <div className="flex flex-wrap items-end gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3">
        <label className="flex flex-1 flex-col gap-1">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
            Add member (subject id)
          </span>
          <input
            value={newId}
            onChange={(e) => setNewId(e.target.value)}
            placeholder="user@acme.com"
            className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm"
          />
        </label>
        <Select
          value={newRole}
          onChange={(e) => setNewRole(e.target.value as TenantMember["role"])}
          className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-2 text-sm"
        >
          {ROLES.map((r) => (
            <option key={r} value={r}>
              {r}
            </option>
          ))}
        </Select>
        <button
          onClick={() => void addMember()}
          disabled={busy === "__add__" || !newId.trim()}
          className="tactile rounded-[var(--radius)] bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-[var(--color-accent-fg,#fff)] disabled:opacity-50"
        >
          {busy === "__add__" ? "Adding…" : "Add"}
        </button>
      </div>

      <div className="overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)]">
        <table className="w-full text-sm">
          <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
            <tr>
              <th className="px-3 py-2">Member</th>
              <th className="px-3 py-2">Joined</th>
              <th className="px-3 py-2">Role</th>
              <th className="px-3 py-2"></th>
            </tr>
          </thead>
          <tbody>
            {members && members.length > 0 ? (
              members.map((m) => {
                const isSelf = m.userId === selfId;
                return (
                  <tr key={m.userId} className="border-t border-[var(--color-border)]">
                    <td className="px-3 py-2">
                      <UserWithEmail id={m.userId} you={isSelf} className="align-middle" />
                      {m.source === "sync" && (
                        <span
                          className="ml-2 rounded-full bg-[var(--color-surface-2)] px-2 py-0.5 text-xs text-[var(--color-fg-subtle)]"
                          title="Managed by organization sync; changing the role makes it a manual membership"
                        >
                          sync
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-2 tabular-nums text-[var(--color-fg-muted)]">
                      {m.joinedAtMs ? new Date(m.joinedAtMs).toLocaleDateString() : "—"}
                    </td>
                    <td className="px-3 py-2">
                      <Select
                        value={m.role}
                        disabled={isSelf || busy === m.userId}
                        onChange={(e) =>
                          void run(
                            m.userId,
                            () => api.setTenantMemberRole(m.userId, e.target.value as TenantMember["role"]),
                            "failed to change role",
                          )
                        }
                        className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1 text-sm disabled:opacity-50"
                        title={isSelf ? "You cannot change your own role" : undefined}
                      >
                        {ROLES.map((r) => (
                          <option key={r} value={r}>
                            {r}
                          </option>
                        ))}
                      </Select>
                    </td>
                    <td className="px-3 py-2 text-right">
                      <button
                        onClick={() => setRemoving(m.userId)}
                        disabled={isSelf || busy === m.userId}
                        className="tactile rounded-[var(--radius)] border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] disabled:opacity-40"
                        title={isSelf ? "You cannot remove yourself" : "Remove member"}
                      >
                        Remove
                      </button>
                    </td>
                  </tr>
                );
              })
            ) : (
              <tr>
                <td colSpan={4} className="px-3 py-8 text-center text-[var(--color-fg-subtle)]">
                  {members ? "No members." : "Loading…"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <ConfirmDialog
        open={removing !== null}
        onClose={() => setRemoving(null)}
        onConfirm={() => removing && run(removing, () => api.removeTenantMember(removing), "failed to remove member")}
        title="Remove this member?"
        description={`${removing ? displayNameOf(removing) : ""} loses access to the organization and its rooms.`}
        confirmLabel="Remove"
        danger
      />
    </section>
  );
}

function QuotaTab() {
  const [runQuota, setRunQuota] = useState("");
  const [costAllowance, setCostAllowance] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      const u = await api.tenantUsage();
      setRunQuota(String(u.runQuota));
      setCostAllowance(String(u.costAllowance));
    } catch (e) {
      setError(errorText(e, "failed to load quota"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async () => {
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      await api.setTenantQuota(Number(runQuota) || 0, Number(costAllowance) || 0);
      setSaved(true);
    } catch (e) {
      setError(errorText(e, "failed to save quota"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="flex max-w-md flex-col gap-4">
      <p className="text-sm text-[var(--color-fg-muted)]">
        Org-level ceilings applied across all borrow grants. Use 0 for unlimited.
      </p>
      {error && <ErrorBox>{error}</ErrorBox>}
      <Field label="Agent run quota">
        <input
          type="number"
          min={0}
          value={runQuota}
          onChange={(e) => setRunQuota(e.target.value)}
          className="w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm"
        />
      </Field>
      <Field label="Cost allowance">
        <input
          type="number"
          min={0}
          step="0.01"
          value={costAllowance}
          onChange={(e) => setCostAllowance(e.target.value)}
          className="w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm"
        />
      </Field>
      <div className="flex items-center gap-3">
        <button
          onClick={() => void save()}
          disabled={saving}
          className="tactile rounded-[var(--radius)] bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-[var(--color-accent-fg,#fff)] disabled:opacity-50"
        >
          {saving ? "Saving…" : "Save quota"}
        </button>
        {saved && <span className="text-sm text-[var(--color-fg-muted)]">Saved.</span>}
      </div>
    </section>
  );
}

function SettingsTab() {
  const [settings, setSettings] = useState<TenantSettings | null>(null);
  const [name, setName] = useState("");
  const [ssoIssuer, setSsoIssuer] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      const s = await api.tenantSettings();
      setSettings(s);
      setName(s.name);
      setSsoIssuer(s.ssoIssuer);
    } catch (e) {
      setError(errorText(e, "failed to load settings"));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async () => {
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      const s = await api.updateTenantSettings({ name, ssoIssuer });
      setSettings(s);
      setSaved(true);
    } catch (e) {
      setError(errorText(e, "failed to save settings"));
    } finally {
      setSaving(false);
    }
  };

  // Policy switches save on toggle (each is one switch, not a form); the
  // members' cached tenant policy is re-read so share actions follow.
  const [policySaving, setPolicySaving] = useState(false);
  const savePolicy = async (patch: { requireSandboxForBorrow?: boolean; publicSharesDisabled?: boolean }) => {
    if (policySaving) return;
    setPolicySaving(true);
    setError(null);
    try {
      setSettings(await api.updateTenantSettings(patch));
      invalidateTenantPolicy();
    } catch (e) {
      setError(errorText(e, "failed to save the policy"));
    } finally {
      setPolicySaving(false);
    }
  };

  return (
    <section className="flex max-w-md flex-col gap-4">
      {error && <ErrorBox>{error}</ErrorBox>}
      <Field label="Tenant ID">
        <input
          value={settings?.tenantId ?? ""}
          disabled
          className="w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2 text-sm text-[var(--color-fg-muted)]"
        />
      </Field>
      <Field label="Display name">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          className="w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm"
        />
      </Field>
      <Field label="SSO issuer / email domain">
        <input
          value={ssoIssuer}
          onChange={(e) => setSsoIssuer(e.target.value)}
          placeholder="acme.com"
          className="w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-sm"
        />
      </Field>
      <div className="flex items-center gap-3">
        <button
          onClick={() => void save()}
          disabled={saving}
          className="tactile rounded-[var(--radius)] bg-[var(--color-accent)] px-4 py-2 text-sm font-medium text-[var(--color-accent-fg,#fff)] disabled:opacity-50"
        >
          {saving ? "Saving…" : "Save settings"}
        </button>
        {saved && <span className="text-sm text-[var(--color-fg-muted)]">Saved.</span>}
      </div>
      <div className="flex flex-col gap-2 border-t border-[var(--color-border)] pt-4">
        <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Agent sandbox</span>
        <Switch
          checked={settings?.requireSandboxForBorrow ?? false}
          onChange={(v) => void savePolicy({ requireSandboxForBorrow: v })}
          label="Require the agent sandbox for borrowed agents"
          ariaLabel="Require the agent sandbox for borrowed agents"
        />
        <p className="text-xs text-[var(--color-fg-subtle)]">
          On: a member's agent can be borrowed only while it runs in its owner's agent sandbox; otherwise the borrow
          is refused with a notice. Off: borrowed agents use the sandbox whenever their owner has it ready, and run as
          before when not.
        </p>
      </div>
      <div className="flex flex-col gap-2 border-t border-[var(--color-border)] pt-4">
        <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Public links</span>
        <Switch
          checked={!(settings?.publicSharesDisabled ?? false)}
          onChange={(v) => void savePolicy({ publicSharesDisabled: !v })}
          label="Allow public share links"
          ariaLabel="Allow public share links"
        />
        <p className="text-xs text-[var(--color-fg-subtle)]">
          On: members can share a deliverable through an expiring link anyone can open without signing in. Off: the
          share actions are hidden and every existing link stops working until this is turned back on. The Public
          links tab lists the active links.
        </p>
      </div>
      <TenantRetentionSection />
    </section>
  );
}

function ceil(v: number | undefined): string {
  return v && v > 0 ? `/ ${v}` : "unlimited";
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">
        {label}
      </span>
      {children}
    </label>
  );
}

function Stat({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3">
      <div className="text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">{label}</div>
      <div className="mt-1 flex items-baseline gap-1">
        <span className="text-lg font-semibold">{value}</span>
        {sub && <span className="text-xs text-[var(--color-fg-subtle)]">{sub}</span>}
      </div>
    </div>
  );
}
