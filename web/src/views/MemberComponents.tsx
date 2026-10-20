import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type MemberComponent } from "@/lib/api";
import { desktop } from "@/lib/desktop";
import { needsSetupText, type DesktopComponentSettings } from "@/lib/componentSettings";
import { providesSkills } from "@/lib/localSkills";
import { ComponentSettingsDialog } from "@/views/settings/ComponentSettingsDialog";
import { SkillsDialog } from "@/views/skills/SkillsPanel";
import { EmptyCard } from "@/ui/Card";
import { PageHeader } from "@/ui/Page";
import { useToast } from "@/ui/Toast";
import { ComponentCard } from "@/views/ComponentCard";
import { ErrorBox } from "@/ui/ErrorBox";
import { errorText } from "@/lib/errors";

// MemberComponents is where a member adds components and sets them up: a
// component's settings open from its card ("Set up" while it needs any),
// and right after it is added when it needs setup, so nobody has to look
// for them elsewhere (docs/prd/2026-10-19-component-settings.md §2.2). A
// component that provides skills (the skills-provider capability) opens its
// skills panel from its card ("Open", in the desktop app), and once by
// itself right after it is added when nothing needs setting first.
export function MemberComponents() {
  const toast = useToast();
  const [components, setComponents] = useState<readonly MemberComponent[] | null>(null);
  // This computer's view of each component's settings (the member's own
  // live here), in the desktop app only.
  const [local, setLocal] = useState<readonly DesktopComponentSettings[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [settingsOf, setSettingsOf] = useState<MemberComponent | null>(null);
  const [skillsOf, setSkillsOf] = useState<string | null>(null);

  const load = useCallback(async (): Promise<{ list: readonly MemberComponent[]; local: readonly DesktopComponentSettings[] }> => {
    setError(null);
    let list: readonly MemberComponent[] = [];
    let mine: readonly DesktopComponentSettings[] = [];
    try {
      const [res, items] = await Promise.all([api.components(), desktop.available() ? desktop.componentSettings().catch(() => null) : Promise.resolve(null)]);
      list = res.components ?? [];
      mine = items ?? [];
    } catch (caught: unknown) {
      setError(errorText(caught, "failed to load components"));
    }
    setComponents(list);
    setLocal(mine);
    return { list, local: mine };
  }, []);

  const setupOf = useCallback(
    (component: MemberComponent, mine: readonly DesktopComponentSettings[] = local) => {
      const here = mine.find((i) => i.kind === component.kind && i.name === component.name);
      const missing = [...new Set([...(component.settingsMissing ?? []), ...(here?.missing ?? [])])];
      return needsSetupText(missing, component.settings ?? []);
    },
    [local],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const groups = useMemo(() => splitComponents(components ?? []), [components]);

  const install = async (component: MemberComponent) => {
    setBusy(component.name);
    setError(null);
    try {
      await api.installComponent(component);
      toast.success(`${component.name} installed`);
      const loaded = await load();
      // Set it up now when it needs anything, else open what it shows.
      const added = loaded.list.find((c) => c.kind === component.kind && c.name === component.name);
      if (added && setupOf(added, loaded.local)) setSettingsOf(added);
      else if (added && opensSkills(added)) setSkillsOf(added.name);
    } catch (caught: unknown) {
      setError(errorText(caught, `failed to install ${component.name}`));
    } finally {
      setBusy(null);
    }
  };

  const remove = async (component: MemberComponent) => {
    setBusy(component.name);
    setError(null);
    try {
      await api.uninstallComponent(component);
      toast.success(`${component.name} removed`);
      await load();
    } catch (caught: unknown) {
      setError(errorText(caught, `failed to remove ${component.name}`));
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <PageHeader
        title="Components"
        subtitle="Components distributed by your admin. Managed components stay active; opt-in components are your choice."
      />
      <div className="p-6">
        <div className="mx-auto flex w-full max-w-[720px] flex-col gap-3">
          {error && <ErrorBox>{error}</ErrorBox>}
          <ComponentSection title="Your components" loading={components === null} emptyTitle="No components installed." emptyHint="Add an opt-in component below, or ask your admin to auto-install one.">
            {groups.yours.map((component) => (
              <ComponentCard
                key={`${component.kind}:${component.name}`}
                mode="member"
                component={component}
                busy={busy === component.name}
                onRemove={(selected) => void remove(selected)}
                onSettings={hasSettings(component) ? setSettingsOf : undefined}
                setup={setupOf(component)}
                onOpen={opensSkills(component) ? (c) => setSkillsOf(c.name) : undefined}
              />
            ))}
          </ComponentSection>
          <ComponentSection title="Available to add" loading={false} emptyTitle="Nothing available to add." emptyHint="Your admin has not published opt-in components for this workspace.">
            {groups.available.map((component) => (
              <ComponentCard
                key={`${component.kind}:${component.name}`}
                mode="member"
                component={component}
                busy={busy === component.name}
                onInstall={(selected) => void install(selected)}
              />
            ))}
          </ComponentSection>
        </div>
      </div>
      <ComponentSettingsDialog
        target={settingsOf}
        onClose={() => {
          setSettingsOf(null);
          void load();
        }}
      />
      <SkillsDialog plugin={skillsOf} onClose={() => setSkillsOf(null)} />
    </>
  );
}

// hasSettings reports a component with anything to set: a declared
// setting, or an MCP tool its member signs in to.
function hasSettings(component: MemberComponent): boolean {
  return (component.settings ?? []).length > 0 || component.mcp?.http?.auth?.scheme === "oauth";
}

// opensSkills: an installed plugin that provides skills, in the desktop app
// (its panel talks to it on this computer).
function opensSkills(component: MemberComponent): boolean {
  return component.installed && component.kind === "plugin" && providesSkills(component) && desktop.available();
}

function splitComponents(components: readonly MemberComponent[]) {
  return {
    yours: components.filter((component) => component.installed),
    available: components.filter((component) => !component.installed),
  };
}

function ComponentSection({
  title,
  loading,
  emptyTitle,
  emptyHint,
  children,
}: {
  readonly title: string;
  readonly loading: boolean;
  readonly emptyTitle: string;
  readonly emptyHint: string;
  readonly children: React.ReactNode;
}) {
  const hasChildren = Array.isArray(children) ? children.length > 0 : Boolean(children);
  return (
    <section className="flex flex-col gap-2">
      <span className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-muted)]">{title}</span>
      {loading ? <EmptyCard title="Loading components…" hint="Fetching your member catalog." /> : hasChildren ? children : <EmptyCard title={emptyTitle} hint={emptyHint} />}
    </section>
  );
}

