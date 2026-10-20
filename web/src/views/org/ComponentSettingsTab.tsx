import { useCallback, useEffect, useState } from "react";
import { needsSetupText } from "@/lib/componentSettings";
import { componentSettingsApi } from "@/lib/componentSettingsApi";
import type { OrgSettingsComponent } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Badge, Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { ComponentSettingsDialog } from "@/views/settings/ComponentSettingsDialog";
import { errorText } from "@/lib/errors";

// Org-admin: the components published to this organization that take
// organization settings (docs/prd/2026-10-19-component-settings.md), of
// any kind (an endpoint component beside the gateway, which asks for its
// values, is in no member's catalog), each with what it still needs;
// Settings opens the same dialog as the component's card on the Components
// page. The values hold for every member of this organization; they are
// stored sealed on the server, a secret is never shown back, and a
// component that runs on members' computers (a plugin, a stdio MCP tool)
// gets them there.
export function ComponentSettingsTab() {
  const [components, setComponents] = useState<readonly OrgSettingsComponent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState<OrgSettingsComponent | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setComponents((await componentSettingsApi.orgComponents()).components);
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
      {(components ?? []).map((c) => {
        const setup = needsSetupText(c.settingsMissing, c.settings);
        return (
          <Card key={`${c.kind}/${c.name}`} className="flex flex-wrap items-center gap-3 px-4 py-3">
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm font-medium">{c.name}</span>
                <Badge tone="neutral" className="tabular">v{c.version}</Badge>
                {setup && <Badge tone="warning">Needs setup</Badge>}
              </div>
              <p className="mt-0.5 text-xs text-[var(--color-fg-subtle)]">{setup || c.description || "Set up."}</p>
            </div>
            <Button size="sm" variant={setup ? "primary" : "outline"} onClick={() => setOpen(c)}>
              {setup ? "Set up" : "Settings"}
            </Button>
          </Card>
        );
      })}
      <ComponentSettingsDialog
        target={open}
        onClose={() => {
          setOpen(null);
          void load();
        }}
      />
    </section>
  );
}
