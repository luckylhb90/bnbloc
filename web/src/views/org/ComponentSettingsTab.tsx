import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type MemberComponent } from "@/lib/api";
import { ofScope, type ComponentSettingsView } from "@/lib/componentSettings";
import { componentSettingsApi } from "@/lib/componentSettingsApi";
import { relativeTime } from "@/lib/format";
import { ErrorBox } from "@/ui/ErrorBox";
import { SettingsForm } from "@/views/settings/SettingsForm";
import { errorText } from "@/lib/errors";

// Org-admin: the organization's settings of the components published to it
// (docs/prd/2026-10-19-component-settings.md), one form per component with
// any organization-scope setting. The values hold for every member of this
// organization; they are stored sealed on the server, a secret is never
// shown back, and a component that runs on members' computers (a plugin, a
// stdio MCP tool) gets them there. A member's own settings are set on their
// desktop instead; the platform's in the platform console.
export function ComponentSettingsTab() {
  const [components, setComponents] = useState<readonly MemberComponent[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const reply = await api.components();
      setComponents((reply.components ?? []).filter((c) => ofScope(c.settings, "org").length > 0));
    } catch (e) {
      setError(errorText(e, "failed to load components"));
      setComponents([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <section className="flex flex-col gap-3 p-6">
      <div>
        <h2 className="text-sm font-semibold">Component settings</h2>
        <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
          Values the components published to your organization need, such as a service URL or an API key. Every member of this organization uses them; they are stored encrypted and never shown back.
        </p>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      {components && components.length === 0 && !error && (
        <p className="text-sm text-[var(--color-fg-subtle)]">No component published to this organization takes organization settings.</p>
      )}
      {(components ?? []).map((c) => (
        <OrgComponentSettings key={`${c.kind}/${c.name}`} component={c} />
      ))}
    </section>
  );
}

// OrgComponentSettings is one component's organization settings.
function OrgComponentSettings({ component }: { readonly component: MemberComponent }) {
  const ref = useMemo(() => ({ name: component.name, kind: component.kind }), [component.name, component.kind]);
  const [view, setView] = useState<ComponentSettingsView | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    componentSettingsApi
      .get("org", ref)
      .then((v) => live && setView(v))
      .catch((e: unknown) => live && setError(errorText(e, `failed to load ${component.name}'s settings`)));
    return () => {
      live = false;
    };
  }, [ref, component.name]);

  if (error) return <ErrorBox>{error}</ErrorBox>;
  if (!view) return null;
  if (!view.available) {
    return (
      <p className="text-sm text-[var(--color-fg-subtle)]">
        {view.name}: organization settings are off on this deployment: the gateway has no secret store key (BNBLOC_SECRET_STORE_KEY). Ask your platform operator.
      </p>
    );
  }
  const updated = view.updatedBy || view.updatedAtMs ? `Saved${view.updatedBy ? ` by ${view.updatedBy}` : ""}${view.updatedAtMs ? ` ${relativeTime(view.updatedAtMs)}` : ""}.` : "Not saved yet.";
  return (
    <SettingsForm
      title={view.name}
      version={view.version}
      status={updated}
      fields={view.fields}
      missing={view.missing.map((s) => s.key)}
      declaration={[...view.fields, ...view.missing]}
      reachesMembers={view.reachesMembers}
      onSave={async (update) => setView(await componentSettingsApi.save("org", ref, update))}
      savedText={`${view.name} settings saved`}
    >
      {view.description && <p className="text-xs text-[var(--color-fg-subtle)]">{view.description}</p>}
    </SettingsForm>
  );
}
