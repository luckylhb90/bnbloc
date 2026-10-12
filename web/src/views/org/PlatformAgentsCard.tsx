import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { cn } from "@/lib/cn";
import {
  consoleAgentRows,
  defaultModelOptions,
  myHostPolicy,
  otherHostPolicies,
  samePlatformAgent,
  samePolicyInput,
  setPolicyDefaultModel,
  toggleDraftAgent,
  type ConsoleAgentRow,
  type ConsoleOwnAgent,
  type PlatformAgentsPolicy,
  type PlatformAgentsPolicyInput,
  type PlatformHostPolicy,
  type PolicyAgent,
} from "@/lib/platformAgents";
import { platformAgentsApi } from "@/lib/platformAgentsApi";
import { modelOptionLabel, splitTrialModels } from "@/lib/platformUsage";
import { Button } from "@/ui/Button";
import { ErrorBox } from "@/ui/ErrorBox";
import { Switch } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { PlatformAgentsUsagePanel } from "@/views/org/PlatformAgentsUsage";
import { UserName } from "@/views/UserName";
import { errorText } from "@/lib/errors";

const EMPTY_ENTRY: PlatformAgentsPolicyInput = { enabled: false, agents: [] };

function entryOf(policy: PlatformAgentsPolicy, subject: string | null): PlatformAgentsPolicyInput {
  const mine = myHostPolicy(policy, subject);
  return mine ? { enabled: mine.enabled, agents: mine.agents } : EMPTY_ENTRY;
}

// PlatformAgentsCard (Platform console overview, super admin): publishes some
// of the admin's own agents platform-wide, each with a default model from the
// admin's own catalog. Every super admin publishes from their own machine:
// the card edits the caller's entry only and shows the other hosts
// read-only. Below, the platform agents' usage.
export function PlatformAgentsCard() {
  const { subject } = useAuth();
  const toast = useToast();
  const [policy, setPolicy] = useState<PlatformAgentsPolicy | null>(null);
  const [mine, setMine] = useState<ConsoleOwnAgent[]>([]);
  const [draft, setDraft] = useState<PlatformAgentsPolicyInput>(EMPTY_ENTRY);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    setError(null);
    try {
      const [p, my] = await Promise.all([platformAgentsApi.policy(), api.myAgents().catch(() => ({ agents: [] }))]);
      setPolicy(p);
      setMine(
        (my.agents ?? []).map((a) => ({
          core: a.core,
          provider: a.provider,
          online: a.online,
          models: a.modelOptions?.models ?? [],
        })),
      );
      setDraft(entryOf(p, subject));
    } catch (e) {
      setError(errorText(e, "Could not load platform agents"));
    }
  }, [subject]);

  useEffect(() => {
    void load();
  }, [load]);

  const saved = policy ? entryOf(policy, subject) : EMPTY_ENTRY;
  const rows = useMemo(() => consoleAgentRows(mine, draft.agents), [mine, draft.agents]);
  const others = policy ? otherHostPolicies(policy, subject) : [];
  const dirty = !!policy && !samePolicyInput(draft, saved);

  const save = async () => {
    setSaving(true);
    try {
      const next = await platformAgentsApi.putPolicy(draft);
      setPolicy(next);
      setDraft(entryOf(next, subject));
      toast.success("Platform agents saved");
    } catch (e) {
      toast.error(errorText(e, "Save failed"));
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Platform agents</h2>
      {error && <ErrorBox>{error}</ErrorBox>}
      {policy && (
        <div className="flex flex-col gap-3 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3 text-sm">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <p className="min-w-0 flex-1 text-[var(--color-fg-muted)]">
              Members of your organizations can add these agents to their rooms and use them without installing
              anything. Runs use your agent core and always run in your sandbox. Pick a default model for each agent —
              a free trial-channel model keeps costs at zero; members may still choose another model.
            </p>
            <Switch
              checked={draft.enabled}
              onChange={(enabled) => setDraft((d) => ({ ...d, enabled }))}
              label={draft.enabled ? "On" : "Off"}
              ariaLabel="Offer my platform agents"
            />
          </div>
          <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
            Published from your machine
          </h3>
          {rows.length === 0 ? (
            <p className="text-xs text-[var(--color-fg-subtle)]">
              Your desktop app advertises no agents yet. Connect an agent core in the desktop app, then reload this
              page.
            </p>
          ) : (
            <div className="flex flex-col divide-y divide-[var(--color-border)] rounded-[var(--radius)] border border-[var(--color-border)]">
              {rows.map((r) => (
                <MyAgentRow
                  key={`${r.core}/${r.provider}`}
                  row={r}
                  published={draft.agents.find((a) => samePlatformAgent(a, r))}
                  disabled={saving}
                  onToggle={(on) => setDraft((d) => toggleDraftAgent(d, r, on))}
                  onDefaultModel={(m) => setDraft((d) => ({ ...d, agents: setPolicyDefaultModel(d.agents, r, m) }))}
                />
              ))}
            </div>
          )}
          {!draft.enabled && draft.agents.length > 0 && (
            <p role="status" className="rounded-[var(--radius)] border border-[var(--color-warning)]/40 bg-[var(--color-warning)]/10 px-3 py-2 text-xs text-[var(--color-fg)]">
              Off — members can't see these agents. Turn the switch on to offer them.
            </p>
          )}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="min-w-0 flex-1 text-xs text-[var(--color-fg-subtle)]">
              Your platform agents answer while your desktop app (agent core and sandbox) is online. An always-on Mac
              signed in as you works best; a second host gives failover.
            </p>
            <Button variant="primary" size="sm" onClick={() => void save()} disabled={!dirty || saving}>
              {saving ? "Saving…" : "Save"}
            </Button>
          </div>
          {others.length > 0 && <OtherHosts hosts={others} />}
        </div>
      )}
      {policy && <PlatformAgentsUsagePanel />}
    </section>
  );
}

function OnlineDot({ online }: { readonly online: boolean }) {
  return (
    <span
      className={cn("size-2 shrink-0 rounded-full", online ? "bg-[var(--color-accent)]" : "bg-[var(--color-fg-subtle)]")}
      title={online ? "Online" : "Offline"}
    />
  );
}

// MyAgentRow is one of the caller's agents: publish it, and (once published)
// choose its default model from the caller's own catalog for it.
function MyAgentRow({
  row,
  published,
  disabled,
  onToggle,
  onDefaultModel,
}: {
  readonly row: ConsoleAgentRow;
  readonly published: PolicyAgent | undefined;
  readonly disabled: boolean;
  readonly onToggle: (on: boolean) => void;
  readonly onDefaultModel: (model: string) => void;
}) {
  const options = defaultModelOptions(row.models, published?.defaultModel);
  const { trial, other } = splitTrialModels(options);
  return (
    <div className="flex flex-col gap-2 px-3 py-2 sm:flex-row sm:items-center">
      <label className="flex min-w-0 flex-1 cursor-pointer items-center gap-3">
        <input
          type="checkbox"
          className="size-4 accent-[var(--color-accent)]"
          checked={!!published}
          disabled={disabled}
          onChange={(e) => onToggle(e.target.checked)}
        />
        <OnlineDot online={row.online} />
        <span className="min-w-0 flex-1 truncate">
          <span className="font-medium text-[var(--color-fg)]">{row.provider}</span>
          <span className="ml-1.5 text-xs text-[var(--color-fg-subtle)]">{row.core || "default core"}</span>
          {!row.mine && (
            <span className="ml-1.5 text-xs text-[var(--color-fg-subtle)]">· not advertised by your desktop now</span>
          )}
        </span>
      </label>
      {published && (
        <label className="flex min-w-0 items-center gap-2 pl-7 text-xs text-[var(--color-fg-subtle)] sm:pl-0">
          <span className="shrink-0">Default model</span>
          <Select
            aria-label={`Default model for ${row.provider}`}
            value={published.defaultModel ?? ""}
            disabled={disabled}
            onChange={(e) => onDefaultModel(e.target.value)}
            className="h-8 min-w-0 max-w-[16rem] flex-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] px-2 text-xs text-[var(--color-fg)]"
          >
            <option value="">Agent&apos;s own default</option>
            {trial.length > 0 && (
              <optgroup label="Free — trial channel">
                {trial.map((m) => (
                  <option key={m} value={m}>
                    {modelOptionLabel(m)}
                  </option>
                ))}
              </optgroup>
            )}
            {other.length > 0 && (
              <optgroup label={trial.length > 0 ? "Other models" : "Models"}>
                {other.map((m) => (
                  <option key={m} value={m}>
                    {modelOptionLabel(m)}
                  </option>
                ))}
              </optgroup>
            )}
          </Select>
        </label>
      )}
    </div>
  );
}

// OtherHosts lists the other super admins' published agents, read-only: each
// host edits their own entry from their own console.
function OtherHosts({ hosts }: { readonly hosts: readonly PlatformHostPolicy[] }) {
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Other hosts</h3>
      <div className="flex flex-col divide-y divide-[var(--color-border)] rounded-[var(--radius)] border border-[var(--color-border)]">
        {hosts.map((h) => (
          <div key={h.hostUserId} className="flex flex-col gap-1 px-3 py-2">
            <div className="flex items-center gap-2">
              <UserName id={h.hostUserId} className="font-medium text-[var(--color-fg)]" />
              <span className="text-xs text-[var(--color-fg-subtle)]">{h.enabled ? "offering" : "off"}</span>
            </div>
            {h.agents.length === 0 ? (
              <p className="text-xs text-[var(--color-fg-subtle)]">No agents published.</p>
            ) : (
              <ul className="flex flex-wrap gap-1.5">
                {h.agents.map((a) => (
                  <li
                    key={`${a.core}/${a.provider}`}
                    className="rounded-full border border-[var(--color-border)] px-2 py-0.5 text-xs text-[var(--color-fg-muted)]"
                  >
                    {a.provider}
                    <span className="text-[var(--color-fg-subtle)]">
                      {" "}
                      · {a.defaultModel ? modelOptionLabel(a.defaultModel) : "agent's own default"}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
