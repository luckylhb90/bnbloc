import { useCallback, useEffect, useState } from "react";
import { desktop } from "@/lib/desktop";
import type { PluginLocalSettings } from "@/lib/pluginSettings";
import { ErrorBox } from "@/ui/ErrorBox";
import { LocalSettingsCard } from "@/views/settings/LocalSettingsCard";
import { errorText } from "@/lib/errors";

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
      setError(errorText(e, "failed to load plugin settings"));
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
  return (
    <LocalSettingsCard
      title={plugin.plugin}
      version={plugin.version}
      status={plugin.configured ? plugin.message : "Not set up yet: fill in the fields below."}
      fields={plugin.fields}
      onSave={async (values) => {
        await desktop.savePluginLocalSettings(plugin.plugin, values);
        await reload();
      }}
      onClear={async () => {
        await desktop.clearPluginLocalSettings(plugin.plugin);
        await reload();
      }}
      savedText={`${plugin.plugin} saved on this computer and restarted`}
      clearedText={`${plugin.plugin} settings removed from this computer`}
    />
  );
}
