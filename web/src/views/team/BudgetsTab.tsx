import { useCallback, useEffect, useMemo, useState } from "react";
import { PlusIcon, TrashIcon, WalletIcon } from "@phosphor-icons/react";
import { api, ApiError } from "@/lib/api";
import { budgetsApi } from "@/lib/budgetsApi";
import {
  buildBudgetsUpdate,
  draftFromView,
  emptyDraft,
  periodLabel,
  type BudgetDraft,
  type BudgetView,
  type TenantBudgets,
} from "@/lib/budgets";
import { compactNumber, formatUsd } from "@/lib/format";
import { Button } from "@/ui/Button";
import { Card, Skeleton } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Input } from "@/ui/Input";
import { EmptyState } from "@/ui/Page";
import { Switch } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { BudgetBar } from "@/views/team/BudgetBar";
import { EmailInput, UserName } from "@/views/UserName";

// BudgetsTab is where org admins set monthly usage budgets (R12): one for the
// whole organization and one per member, each a token and/or cost limit with
// a warning threshold and an optional hard stop. Burn is the usage agents
// reported on their replies this month (UTC); borrowed runs count against the
// agent's owner.
export function BudgetsTab() {
  const toast = useToast();
  const [data, setData] = useState<TenantBudgets | null>(null);
  const [error, setError] = useState("");
  const [tenantDraft, setTenantDraft] = useState<BudgetDraft | null>(null);
  const [memberDrafts, setMemberDrafts] = useState<BudgetDraft[]>([]);
  const [members, setMembers] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);

  const apply = useCallback((d: TenantBudgets) => {
    setData(d);
    setTenantDraft(d.tenant ? draftFromView(d.tenant) : null);
    setMemberDrafts(d.members.map(draftFromView));
    setDirty(false);
  }, []);

  const load = useCallback(() => {
    budgetsApi
      .tenant()
      .then((d) => {
        apply(d);
        setError("");
      })
      .catch((e) => setError(e instanceof ApiError ? e.message : "Could not load budgets"));
  }, [apply]);
  useEffect(load, [load]);

  useEffect(() => {
    api
      .tenantMembers()
      .then((r) => setMembers(r.members.map((m) => m.userId).sort()))
      .catch(() => {});
  }, []);

  const views = useMemo(() => {
    const byUser = new Map<string, BudgetView>();
    for (const v of data?.members ?? []) byUser.set(v.userId ?? "", v);
    return byUser;
  }, [data]);

  const editMember = (i: number, patch: Partial<BudgetDraft>) => {
    setMemberDrafts((rows) => rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    setDirty(true);
  };

  const save = async () => {
    const { update, error: invalid } = buildBudgetsUpdate(tenantDraft, memberDrafts);
    if (!update) {
      toast.error(invalid ?? "Check the budgets");
      return;
    }
    setSaving(true);
    try {
      apply(await budgetsApi.saveTenant(update));
      toast.success("Budgets saved");
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : "Could not save budgets");
    } finally {
      setSaving(false);
    }
  };

  if (error) return <ErrorBox>{error}</ErrorBox>;
  if (!data) return <Skeleton className="h-40" />;

  const budgeted = new Set(memberDrafts.map((d) => d.userId.trim()));
  const available = members.filter((m) => !budgeted.has(m));

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-3">
        <p className="min-w-0 flex-1 text-sm text-[var(--color-fg-muted)]">
          {periodLabel(data.period)} · burn is the usage agents reported on their replies; runs borrowed on a member's
          agent count against that member. Warnings go to the member (organization budget: to org admins).
        </p>
        <Button variant="ghost" size="sm" disabled={!dirty || saving} onClick={() => apply(data)}>
          Discard
        </Button>
        <Button variant="primary" size="sm" disabled={!dirty || saving} onClick={() => void save()}>
          {saving ? "Saving…" : "Save budgets"}
        </Button>
      </div>

      <section className="flex flex-col gap-3">
        <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Organization</h2>
        {tenantDraft ? (
          <Card className="flex flex-col gap-3 px-4 py-3">
            <BudgetFields
              draft={tenantDraft}
              onChange={(patch) => {
                setTenantDraft({ ...tenantDraft, ...patch });
                setDirty(true);
              }}
              onRemove={() => {
                setTenantDraft(null);
                setDirty(true);
              }}
              hardStopHint="Stops every agent run in the organization"
            />
            {data.tenant && <BudgetBar view={data.tenant} />}
          </Card>
        ) : (
          <Card className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
            <span className="text-sm text-[var(--color-fg-muted)]">No organization budget.</span>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setTenantDraft(emptyDraft());
                setDirty(true);
              }}
            >
              <PlusIcon size={14} /> Add organization budget
            </Button>
          </Card>
        )}
      </section>

      <section className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="flex-1 text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Members</h2>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setMemberDrafts((rows) => [...rows, emptyDraft(available[0] ?? "")]);
              setDirty(true);
            }}
          >
            <PlusIcon size={14} /> Add member budget
          </Button>
        </div>
        {memberDrafts.length === 0 ? (
          <EmptyState
            icon={<WalletIcon size={26} />}
            title="No member budgets"
            hint="Give a member a monthly token or cost budget to be warned (or stop their agents) as they approach it."
          />
        ) : (
          <div className="flex flex-col gap-3">
            {memberDrafts.map((d, i) => {
              const view = views.get(d.userId.trim());
              return (
                <Card key={i} className="flex flex-col gap-3 px-4 py-3">
                  <label className="flex flex-col gap-1 text-xs text-[var(--color-fg-muted)] sm:max-w-xs">
                    Member
                    <EmailInput
                      list="budget-members"
                      value={d.userId}
                      onChange={(e) => editMember(i, { userId: e.target.value })}
                      placeholder="Member id"
                      className="h-9"
                    />
                  </label>
                  <BudgetFields
                    draft={d}
                    onChange={(patch) => editMember(i, patch)}
                    onRemove={() => {
                      setMemberDrafts((rows) => rows.filter((_, j) => j !== i));
                      setDirty(true);
                    }}
                    hardStopHint="Stops this member's agents, including runs others borrow"
                  />
                  {view && <BudgetBar view={view} />}
                </Card>
              );
            })}
          </div>
        )}
        <datalist id="budget-members">
          {available.map((m) => (
            <option key={m} value={m} />
          ))}
        </datalist>
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Usage this month</h2>
        {data.usage.length === 0 ? (
          <p className="text-sm text-[var(--color-fg-subtle)]">No agent reported usage this month yet.</p>
        ) : (
          <div className="overflow-x-auto rounded-[var(--radius)] border border-[var(--color-border)]">
            <table className="w-full min-w-[28rem] text-sm">
              <thead className="bg-[var(--color-surface-2)] text-left text-xs uppercase tracking-wide text-[var(--color-fg-subtle)]">
                <tr>
                  <th className="px-3 py-2">Member</th>
                  <th className="px-3 py-2 text-right">Tokens</th>
                  <th className="px-3 py-2 text-right">Cost</th>
                  <th className="px-3 py-2 text-right">Budget used</th>
                </tr>
              </thead>
              <tbody>
                {data.usage.map((u) => {
                  const v = views.get(u.userId);
                  return (
                    <tr key={u.userId} className="border-t border-[var(--color-border)]">
                      <td className="px-3 py-2">
                        <UserName id={u.userId} />
                      </td>
                      <td className="px-3 py-2 text-right tabular">{compactNumber(u.tokens)}</td>
                      <td className="px-3 py-2 text-right tabular">{formatUsd(u.cost)}</td>
                      <td className="px-3 py-2 text-right tabular">{v ? `${v.percent}%` : "—"}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}

// BudgetFields edits one budget's limits.
function BudgetFields({
  draft,
  onChange,
  onRemove,
  hardStopHint,
}: {
  readonly draft: BudgetDraft;
  readonly onChange: (patch: Partial<BudgetDraft>) => void;
  readonly onRemove: () => void;
  readonly hardStopHint: string;
}) {
  return (
    <div className="flex flex-wrap items-end gap-3">
      <label className="flex w-36 flex-col gap-1 text-xs text-[var(--color-fg-muted)]">
        Tokens / month
        <Input
          inputMode="numeric"
          value={draft.tokens}
          onChange={(e) => onChange({ tokens: e.target.value })}
          placeholder="No limit"
          className="h-9"
        />
      </label>
      <label className="flex w-32 flex-col gap-1 text-xs text-[var(--color-fg-muted)]">
        Cost / month ($)
        <Input
          inputMode="decimal"
          value={draft.cost}
          onChange={(e) => onChange({ cost: e.target.value })}
          placeholder="No limit"
          className="h-9"
        />
      </label>
      <label className="flex w-24 flex-col gap-1 text-xs text-[var(--color-fg-muted)]">
        Warn at (%)
        <Input
          inputMode="numeric"
          value={draft.warnAtPercent}
          onChange={(e) => onChange({ warnAtPercent: e.target.value })}
          className="h-9"
        />
      </label>
      <div className="flex flex-col gap-1 pb-1.5 text-xs text-[var(--color-fg-muted)]" title={hardStopHint}>
        Hard stop
        <Switch
          checked={draft.hardStop}
          onChange={(hardStop) => onChange({ hardStop })}
          label={draft.hardStop ? "On" : "Off"}
          ariaLabel="Hard stop"
        />
      </div>
      <Button variant="ghost" size="icon" className="ml-auto" onClick={onRemove} aria-label="Remove budget" title="Remove budget">
        <TrashIcon size={16} />
      </Button>
    </div>
  );
}
