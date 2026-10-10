import { useCallback, useEffect, useState } from "react";
import { desktop } from "@/lib/desktop";
import { initialValues, missingRequired, settingsUpdate, type PluginLocalSettings } from "@/lib/pluginSettings";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";

// PersonalBots configures the member's own notification delivery: a plugin
// (installed by the platform admin, turned on in Components when optional)
// that runs inside the desktop app and sends the member's notifications
// through their own bot. Its settings are stored on this computer by the
// desktop shell and never sent to the server. In a browser there is nothing
// to configure: personal bots only run in the desktop app.
export function PersonalBots() {
  const inShell = desktop.available();
  const [plugins, setPlugins] = useState<PluginLocalSettings[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setPlugins(await desktop.pluginLocalSettings());
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load plugin settings");
      setPlugins([]);
    }
  }, []);

  useEffect(() => {
    if (inShell) void load();
  }, [inShell, load]);

  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold">Personal bot</h2>
        <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
          Get your notifications from your own bot, and reply to them there. It runs in the desktop app while it is open; otherwise your organization's delivery is used.
        </p>
      </div>
      {!inShell && (
        <p className="text-sm text-[var(--color-fg-subtle)]">
          Personal bots run in the bnbloc desktop app. Open Settings → Notifications there to set yours up.
        </p>
      )}
      {inShell && error && <ErrorBox>{error}</ErrorBox>}
      {inShell && plugins && plugins.length === 0 && !error && (
        <p className="text-sm text-[var(--color-fg-subtle)]">
          No personal bot plugin is installed for you. If your organization offers one, turn it on in Components (or ask your admin).
        </p>
      )}
      {(plugins ?? []).map((p) => (
        <PluginSettingsCard key={p.plugin} plugin={p} reload={load} />
      ))}
    </section>
  );
}

function PluginSettingsCard({ plugin, reload }: { plugin: PluginLocalSettings; reload: () => Promise<void> }) {
  const toast = useToast();
  const [values, setValues] = useState<Record<string, string>>(() => initialValues(plugin.fields));
  const [busy, setBusy] = useState<"save" | "clear" | null>(null);

  useEffect(() => {
    setValues(initialValues(plugin.fields));
  }, [plugin]);

  const run = async (kind: NonNullable<typeof busy>, fn: () => Promise<void>) => {
    setBusy(kind);
    try {
      await fn();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Something went wrong");
    } finally {
      setBusy(null);
    }
  };

  const save = () =>
    run("save", async () => {
      const missing = missingRequired(plugin.fields, values);
      if (missing.length > 0) {
        toast.error(`Fill in ${missing.join(", ")}`);
        return;
      }
      await desktop.savePluginLocalSettings(plugin.plugin, settingsUpdate(plugin.fields, values));
      toast.success(`${plugin.plugin} saved on this computer and restarted`);
      await reload();
    });

  const clear = () =>
    run("clear", async () => {
      await desktop.clearPluginLocalSettings(plugin.plugin);
      toast.success(`${plugin.plugin} settings removed from this computer`);
      await reload();
    });

  return (
    <Card className="flex flex-col gap-3 px-4 py-3">
      <div className="min-w-0">
        <span className="block text-sm font-medium">
          {plugin.plugin} <span className="text-xs font-normal text-[var(--color-fg-subtle)]">{plugin.version}</span>
        </span>
        <span className="block text-xs text-[var(--color-fg-muted)]">{plugin.configured ? plugin.message : "Not set up yet: fill in the fields below."}</span>
      </div>
      <div className="flex flex-col gap-3">
        {plugin.fields.map((f) => (
          <Field key={f.key} label={f.required ? `${f.label} *` : f.label} hint={f.help}>
            <Input
              type={f.secret ? "password" : "text"}
              autoComplete={f.secret ? "new-password" : "off"}
              spellCheck={false}
              value={values[f.key] ?? ""}
              placeholder={f.secret && f.set ? "••• set (leave empty to keep)" : ""}
              onChange={(e) => setValues((cur) => ({ ...cur, [f.key]: e.target.value }))}
            />
          </Field>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="primary" disabled={busy !== null} onClick={() => void save()}>
          {busy === "save" ? "Saving…" : "Save"}
        </Button>
        {plugin.fields.some((f) => f.set) && (
          <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void clear()}>
            {busy === "clear" ? "Removing…" : "Remove from this computer"}
          </Button>
        )}
      </div>
    </Card>
  );
}
