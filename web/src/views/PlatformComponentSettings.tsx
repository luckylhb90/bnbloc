import { useEffect, useState } from "react";
import type { PlatformComponent } from "@/lib/api";
import type { ComponentSettingsView } from "@/lib/componentSettings";
import { componentSettingsApi } from "@/lib/componentSettingsApi";
import { relativeTime } from "@/lib/format";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { SettingsForm } from "@/views/settings/SettingsForm";
import { errorText } from "@/lib/errors";

// PlatformComponentSettings is a component's platform-scope settings in the
// platform console (docs/prd/2026-10-19-component-settings.md): the values
// the super admin sets for the whole deployment, stored sealed and never
// shown back when secret. Organization and member settings are set in their
// own places.
export function PlatformComponentSettings({ component, onClose }: { readonly component: PlatformComponent; readonly onClose: () => void }) {
  const [view, setView] = useState<ComponentSettingsView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const ref = { name: component.name, kind: component.kind };

  useEffect(() => {
    let live = true;
    componentSettingsApi
      .get("platform", { name: component.name, kind: component.kind })
      .then((v) => live && setView(v))
      .catch((e: unknown) => live && setError(errorText(e, "failed to load the settings")));
    return () => {
      live = false;
    };
  }, [component.name, component.kind]);

  const updated = view && (view.updatedBy || view.updatedAtMs) ? `Saved${view.updatedBy ? ` by ${view.updatedBy}` : ""}${view.updatedAtMs ? ` ${relativeTime(view.updatedAtMs)}` : ""}.` : "Not saved yet.";
  return (
    <Dialog open onClose={onClose} title={`${component.name} settings`} description="Platform settings hold for every organization." size="lg">
      {error && <ErrorBox>{error}</ErrorBox>}
      {view && !view.available && (
        <p className="text-sm text-[var(--color-fg-subtle)]">Settings are off on this deployment: the gateway has no secret store key (BNBLOC_SECRET_STORE_KEY).</p>
      )}
      {view?.available && (
        <SettingsForm
          title={view.name}
          version={view.version}
          status={updated}
          fields={view.fields}
          missing={view.missing.map((s) => s.key)}
          declaration={[...view.fields, ...view.missing]}
          reachesMembers={view.reachesMembers}
          onSave={async (update) => setView(await componentSettingsApi.save("platform", ref, update))}
          savedText={`${view.name} settings saved`}
        />
      )}
    </Dialog>
  );
}
