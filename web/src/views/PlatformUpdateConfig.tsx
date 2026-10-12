import { useCallback, useEffect, useState } from "react";
import { WarningCircleIcon } from "@phosphor-icons/react";
import { api, ApiError, type UpdateConfig, type UpdatePlatformArtifact } from "@/lib/api";
import { Button } from "@/ui/Button";
import { Card, Badge } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment, Switch } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { DesktopPublishWizard } from "@/views/DesktopPublishWizard";
import { completeUpdateConfig, fieldErrorsFromUpdateMessage, MANUAL_UPDATE_TARGETS, MAX_UPDATE_CHECK_INTERVAL_HOURS, MIN_UPDATE_CHECK_INTERVAL_HOURS, updateConfigDirty, type UpdateFieldErrors, type UpdateTarget } from "@/views/updateConfigFields";
import { errorText } from "@/lib/errors";

const CHANNEL_OPTIONS = [
  { value: "stable", label: "Stable" },
  { value: "internal", label: "Internal" },
] as const;

const EMPTY_CONFIG: UpdateConfig = completeUpdateConfig({
  policy: {
    enabled: false,
    channel: "stable",
    autoCheck: true,
    checkIntervalHours: 24,
    autoInstall: false,
    minSupportedVersion: "",
  },
  release: { version: "", notes: "", pubDate: "", platforms: {} },
});

export function PlatformUpdateConfig() {
  const toast = useToast();
  // `config` is the editable form; `saved` is the last config loaded from or
  // saved to the gateway. The publish wizard works from `saved` only, so an
  // unsaved edit can never be silently discarded by a publish.
  const [config, setConfig] = useState<UpdateConfig>(EMPTY_CONFIG);
  const [saved, setSaved] = useState<UpdateConfig>(EMPTY_CONFIG);
  const [errors, setErrors] = useState<UpdateFieldErrors>({});
  const [loadError, setLoadError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const dirty = updateConfigDirty(saved, config);

  const adopt = (next: UpdateConfig) => {
    const complete = completeUpdateConfig(next);
    setConfig(complete);
    setSaved(complete);
  };

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const complete = completeUpdateConfig(await api.getUpdateConfig());
      setConfig(complete);
      setSaved(complete);
      setErrors({});
    } catch (e: unknown) {
      setLoadError(errorText(e, "failed to load update configuration"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const patchPolicy = (patch: Partial<UpdateConfig["policy"]>) => {
    setConfig((current) => ({ ...current, policy: { ...current.policy, ...patch } }));
  };
  const patchTarget = (target: UpdateTarget, patch: Partial<UpdatePlatformArtifact>) => {
    setConfig((current) => ({
      ...current,
      release: {
        ...current.release,
        platforms: {
          ...current.release.platforms,
          [target]: { ...(current.release.platforms[target] ?? { url: "", signature: "" }), ...patch },
        },
      },
    }));
  };

  const save = async () => {
    setSaving(true);
    setErrors({});
    setLoadError(null);
    try {
      adopt(await api.setUpdateConfig(config));
      toast.success("Update configuration saved");
    } catch (e: unknown) {
      if (e instanceof ApiError && e.status === 400) {
        const next = fieldErrorsFromUpdateMessage(e.message);
        setErrors(next);
        if (Object.keys(next).length === 0) setLoadError(e.message);
      } else {
        setLoadError(errorText(e, "failed to save update configuration"));
      }
    } finally {
      setSaving(false);
    }
  };
  const discard = () => {
    setConfig(saved);
    setErrors({});
  };

  if (loading) {
    return <p className="text-sm text-[var(--color-fg-muted)]">Loading update configuration…</p>;
  }

  return (
    <section className="flex flex-col gap-4">
      {loadError && <ErrorBox>{loadError}</ErrorBox>}
      <Card className="p-4">
        <SectionHeading title="Policy" subtitle="Rarely changed rollout rules for every desktop client." />
        <div className="mt-4 grid gap-4 md:grid-cols-2">
          <Field label="Updates" hint="Off means clients do not poll or prompt.">
            <Switch checked={config.policy.enabled} onChange={(enabled) => patchPolicy({ enabled })} label={config.policy.enabled ? "Enabled" : "Disabled"} />
          </Field>
          <Field label="Channel" error={errors["policy.channel"]}>
            <Segment options={CHANNEL_OPTIONS} value={config.policy.channel} onChange={(channel) => patchPolicy({ channel })} />
          </Field>
          {config.policy.channel === "internal" && (
            <div className="md:col-span-2 rounded-[var(--radius)] border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 p-3 text-sm text-[var(--color-warning)]">
              <WarningCircleIcon size={16} weight="fill" className="mr-1 inline" /> Internal ships the company build with bundled internal services. Do not use it for stable clients.
            </div>
          )}
          <Field label="Automatic checks" hint="Manual checks remain available when this is off.">
            <Switch checked={config.policy.autoCheck} onChange={(autoCheck) => patchPolicy({ autoCheck })} label={config.policy.autoCheck ? "On" : "Off"} />
          </Field>
          <Field label="Check interval hours" hint={`Between ${MIN_UPDATE_CHECK_INTERVAL_HOURS} and ${MAX_UPDATE_CHECK_INTERVAL_HOURS} hours.`} error={errors["policy.checkIntervalHours"]}>
            <Input type="number" min={MIN_UPDATE_CHECK_INTERVAL_HOURS} max={MAX_UPDATE_CHECK_INTERVAL_HOURS} value={config.policy.checkIntervalHours} onChange={(e) => patchPolicy({ checkIntervalHours: Number(e.target.value) || 0 })} />
          </Field>
          <Field label="Auto-install" hint="When on, clients install eligible updates without prompting.">
            <Switch checked={config.policy.autoInstall} onChange={(autoInstall) => patchPolicy({ autoInstall })} label={config.policy.autoInstall ? "On" : "Off"} />
          </Field>
          <Field label="Minimum supported version" hint="Clients below this semver see a required update." error={errors["policy.minSupportedVersion"]}>
            <Input value={config.policy.minSupportedVersion} placeholder="0.1.2" onChange={(e) => patchPolicy({ minSupportedVersion: e.target.value })} />
          </Field>
        </div>
      </Card>

      <DesktopPublishWizard
        saved={saved}
        dirty={dirty}
        saving={saving}
        onSaveChanges={() => void save()}
        onDiscardChanges={discard}
        onPublished={adopt}
        onPartialFailure={setLoadError}
      />

      <Card className="p-4">
        <SectionHeading title="Other platforms (optional)" subtitle="For manually hosted Windows and Intel Mac artifacts. The wizard above manages this computer’s Apple Silicon updater." />
        <details className="mt-4">
          <summary className="cursor-pointer text-sm text-[var(--color-fg-muted)]">Configure manual platform artifacts</summary>
          <div className="mt-4 flex flex-col gap-3">
            {MANUAL_UPDATE_TARGETS.map((target) => {
            const artifact = config.release.platforms[target] ?? { url: "", signature: "" };
            return (
              <div key={target} className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
                <div className="mb-3 flex items-center gap-2">
                  <span className="tabular text-sm font-medium">{target}</span>
                  <Badge tone="neutral">Tauri target</Badge>
                </div>
                <div className="grid gap-3 md:grid-cols-2">
                  <Field label="Installer URL" error={errors[`release.platforms.${target}.url`]}>
                    <Input value={artifact.url} placeholder="https://files.example.com/bnbloc.dmg" onChange={(e) => patchTarget(target, { url: e.target.value })} />
                  </Field>
                  <Field label="Signature (.sig file contents)" hint="Paste the full text produced by the signed Tauri build." error={errors[`release.platforms.${target}.signature`]}>
                    <Textarea rows={3} value={artifact.signature} onChange={(e) => patchTarget(target, { signature: e.target.value })} className="font-mono text-xs" />
                  </Field>
                </div>
              </div>
            );
            })}
          </div>
        </details>
      </Card>

      <div className="flex justify-end gap-2">
        {dirty && (
          <Button variant="ghost" onClick={discard} disabled={saving}>
            Discard changes
          </Button>
        )}
        <Button variant="primary" onClick={() => void save()} disabled={saving || !dirty}>
          {saving ? "Saving…" : "Save update configuration"}
        </Button>
      </div>
    </section>
  );
}

function SectionHeading({ title, subtitle }: { readonly title: string; readonly subtitle: string }) {
  return (
    <div>
      <h2 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg)]">{title}</h2>
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{subtitle}</p>
    </div>
  );
}
