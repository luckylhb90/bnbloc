import { useCallback, useEffect, useState } from "react";
import { ApiError } from "@/lib/api";
import {
  daysOf,
  describeDays,
  describeEffective,
  describeStatus,
  fieldsFromPolicy,
  policyFromFields,
  RETENTION_KINDS,
  samePolicy,
  type PlatformRetentionView,
  type RetentionFields,
  type RetentionLayer,
  type RetentionLimits,
  type RetentionMode,
  type RetentionPolicy,
  type TenantRetentionView,
} from "@/lib/retention";
import { retentionApi } from "@/lib/retentionApi";
import { Button } from "@/ui/Button";
import { ErrorBox } from "@/ui/ErrorBox";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";

// Data retention settings: the organization's override in the org Settings
// tab (TenantRetentionSection) and the platform default in the Platform
// console overview (PlatformRetentionCard). Both say plainly what is deleted
// and when; the accepted day range comes from the server.

const INTRO =
  "Data older than the period you set is deleted for good by a background cleanup, and stored files nothing uses any more are removed. Deleted data cannot be restored.";

export function TenantRetentionSection({ scope }: { readonly scope?: string }) {
  const toast = useToast();
  const [view, setView] = useState<TenantRetentionView | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setView(await retentionApi.tenant(scope));
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load data retention");
    }
  }, [scope]);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async (policy: RetentionPolicy) => {
    try {
      setView(await retentionApi.saveTenant(policy, scope));
      toast.success("Data retention saved");
      return true;
    } catch (e) {
      toast.error(e instanceof ApiError ? `Save failed: ${e.message}` : "Save failed");
      return false;
    }
  };

  return (
    <div className="flex flex-col gap-2 border-t border-[var(--color-border)] pt-4">
      <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Data retention</span>
      <p className="text-xs text-[var(--color-fg-subtle)]">
        {INTRO} &ldquo;Platform default&rdquo; follows the period your platform administrator set.
      </p>
      {error && <ErrorBox>{error}</ErrorBox>}
      {view && (
        <RetentionForm
          layer="tenant"
          saved={view.override}
          limits={view.limits}
          defaultPolicy={view.default}
          describe={(kind) => describeEffective(view, kind)}
          onSave={save}
        />
      )}
    </div>
  );
}

export function PlatformRetentionCard() {
  const toast = useToast();
  const [view, setView] = useState<PlatformRetentionView | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setView(await retentionApi.platform());
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load data retention");
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async (policy: RetentionPolicy) => {
    try {
      setView(await retentionApi.savePlatform(policy));
      toast.success("Default data retention saved");
      return true;
    } catch (e) {
      toast.error(e instanceof ApiError ? `Save failed: ${e.message}` : "Save failed");
      return false;
    }
  };

  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-sm font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">Data retention</h2>
      {error && <ErrorBox>{error}</ErrorBox>}
      {view && (
        <div className="flex flex-col gap-3 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-4 py-3 text-sm">
          <p className="text-[var(--color-fg-muted)]">
            The default for every organization. An organization admin can choose a different period for their own
            organization. {INTRO}
          </p>
          <RetentionForm
            layer="platform"
            saved={view.default}
            limits={view.limits}
            describe={(kind) => `${describeDays(daysOf(view.default, kind))}.`}
            onSave={save}
          />
          <p className="text-xs text-[var(--color-fg-subtle)]">{describeStatus(view.status)}</p>
        </div>
      )}
    </section>
  );
}

// RetentionForm edits one policy: per kind, keep forever, use the platform
// default (organization only) or delete after a number of days.
function RetentionForm({
  layer,
  saved,
  limits,
  defaultPolicy,
  describe,
  onSave,
}: {
  readonly layer: RetentionLayer;
  readonly saved: RetentionPolicy;
  readonly limits: RetentionLimits;
  readonly defaultPolicy?: RetentionPolicy;
  readonly describe: (kind: (typeof RETENTION_KINDS)[number]["kind"]) => string;
  readonly onSave: (policy: RetentionPolicy) => Promise<boolean>;
}) {
  const [fields, setFields] = useState<RetentionFields>(() => fieldsFromPolicy(saved, layer));
  const [saving, setSaving] = useState(false);
  useEffect(() => setFields(fieldsFromPolicy(saved, layer)), [saved, layer]);

  const parsed = policyFromFields(fields, layer, limits);
  const dirty = "policy" in parsed ? !samePolicy(parsed.policy, saved) : true;

  const submit = async () => {
    if (!("policy" in parsed)) return;
    setSaving(true);
    await onSave(parsed.policy);
    setSaving(false);
  };

  const setMode = (kind: keyof RetentionFields, mode: RetentionMode) =>
    setFields((f) => ({ ...f, [kind]: { mode, days: mode === "days" ? f[kind].days || String(limits.minDays) : "" } }));

  return (
    <div className="flex flex-col gap-3">
      {RETENTION_KINDS.map(({ kind, label, explains }) => {
        const f = fields[kind];
        const inherited = defaultPolicy ? describeDays(daysOf(defaultPolicy, kind)).toLowerCase() : "";
        return (
          <div key={kind} className="flex flex-col gap-1.5">
            <div className="flex flex-wrap items-center gap-2">
              <span className="min-w-0 flex-1 text-sm font-medium text-[var(--color-fg)]">{label}</span>
              <Select
                value={f.mode}
                disabled={saving}
                aria-label={`${label} retention`}
                onChange={(e) => setMode(kind, e.target.value as RetentionMode)}
                className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5 text-sm"
              >
                {layer === "tenant" && <option value="default">Platform default ({inherited})</option>}
                <option value="forever">Keep forever</option>
                <option value="days">Delete after…</option>
              </Select>
              {f.mode === "days" && (
                <label className="flex items-center gap-1.5 text-sm text-[var(--color-fg-muted)]">
                  <input
                    type="number"
                    inputMode="numeric"
                    min={limits.minDays}
                    max={limits.maxDays}
                    step={1}
                    value={f.days}
                    disabled={saving}
                    aria-label={`${label}: days to keep`}
                    onChange={(e) => setFields((all) => ({ ...all, [kind]: { mode: "days", days: e.target.value } }))}
                    className="w-24 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1.5 text-sm"
                  />
                  days
                </label>
              )}
            </div>
            <p className="text-xs text-[var(--color-fg-subtle)]">
              {explains} <span className="text-[var(--color-fg-muted)]">{describe(kind)}</span>
            </p>
          </div>
        );
      })}
      {"error" in parsed && <p className="text-xs text-[var(--color-danger)]">{parsed.error}</p>}
      <div className="flex items-center justify-between gap-2">
        <p className="min-w-0 flex-1 text-xs text-[var(--color-fg-subtle)]">
          Periods are between {limits.minDays} and {limits.maxDays.toLocaleString("en-US")} days.
        </p>
        <Button variant="primary" size="sm" onClick={() => void submit()} disabled={!dirty || saving || "error" in parsed}>
          {saving ? "Saving…" : "Save retention"}
        </Button>
      </div>
    </div>
  );
}
