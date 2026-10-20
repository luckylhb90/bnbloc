import { useCallback, useEffect, useState } from "react";
import { useAuth } from "@/lib/auth";
import { desktop } from "@/lib/desktop";
import { signInLine } from "@/lib/mcpTools";
import { can } from "@/lib/permissions";
import {
  needsSetupText,
  ofScope,
  settingLabels,
  type ComponentSetting,
  type ComponentSettingScope,
  type ComponentSettingsPage,
  type ComponentSettingsView,
  type DesktopComponentSettings,
} from "@/lib/componentSettings";
import { componentSettingsApi, type SettingsOwner } from "@/lib/componentSettingsApi";
import { memberPageView, scopePageView, type SettingsPageScope } from "@/lib/settingsPageFrame";
import { relativeTime } from "@/lib/format";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { SettingsForm } from "@/views/settings/SettingsForm";
import { SettingsPageFrame } from "@/views/settings/SettingsPageFrame";
import { errorText } from "@/lib/errors";

// ComponentSettingsDialog is a component's settings, opened where the
// component is (its card on the Components page, the platform console, the
// organization's list) so nobody has to know where else to look
// (docs/prd/2026-10-19-component-settings.md §2.2). It shows each scope the
// viewer may set: their own (on this computer, in the desktop app), the
// organization's (an org admin), the platform's (a super admin); a scope
// they may not set but that is missing says who sets it. A component with
// its own settings page shows that page in each scope's place instead of
// the one form (SettingsPageFrame), each editing only its scope.

/** The component the dialog is for, as the catalogs list it. */
export interface SettingsTarget {
  readonly kind: string;
  readonly name: string;
  readonly settings?: readonly ComponentSetting[];
  readonly settingsPage?: ComponentSettingsPage;
}

export function ComponentSettingsDialog({ target, onClose }: { readonly target: SettingsTarget | null; readonly onClose: () => void }) {
  if (!target) return null;
  return (
    <Dialog open onClose={onClose} title={`${target.name} settings`} description="Set it up here; it runs once every required setting is set." size="lg">
      <SettingsSections target={target} onClose={onClose} />
    </Dialog>
  );
}

function SettingsSections({ target, onClose }: { readonly target: SettingsTarget; readonly onClose: () => void }) {
  const { role } = useAuth();
  const declared = target.settings ?? [];
  const owners: SettingsOwner[] = [];
  if (can(role, "manage_org") && ofScope(declared, "org").length > 0) owners.push("org");
  if (can(role, "manage_platform") && ofScope(declared, "platform").length > 0) owners.push("platform");
  const [views, setViews] = useState<Partial<Record<SettingsOwner, ComponentSettingsView>>>({});
  const [mine, setMine] = useState<DesktopComponentSettings | null | undefined>(desktop.available() ? undefined : null);
  const [error, setError] = useState<string | null>(null);
  const ref = { kind: target.kind, name: target.name };
  const key = `${target.kind}/${target.name}/${owners.join(",")}`;

  const load = useCallback(async () => {
    setError(null);
    try {
      const [loaded, items] = await Promise.all([
        Promise.all(owners.map(async (o) => [o, await componentSettingsApi.get(o, ref)] as const)),
        desktop.available() ? desktop.componentSettings() : Promise.resolve(null),
      ]);
      setViews(Object.fromEntries(loaded));
      setMine((items ?? []).find((i) => i.kind === target.kind && i.name === target.name) ?? null);
    } catch (e) {
      setError(errorText(e, "failed to load the settings"));
    }
    // ref and owners are this target's: key names them.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  useEffect(() => {
    void load();
  }, [load]);

  const memberDeclared = ofScope(declared, "member").length > 0 || mine?.auth === "oauth";
  const othersMissing = (mine?.missing ?? []).filter((k) => {
    const s = declared.find((d) => d.key === k);
    return s && s.scope !== "member" && !owners.includes(s.scope === "org" ? "org" : "platform");
  });
  return (
    <div className="flex flex-col gap-4">
      {error && <ErrorBox>{error}</ErrorBox>}
      {declared.length === 0 && mine?.auth !== "oauth" && <p className="text-sm text-[var(--color-fg-subtle)]">This component has nothing to set.</p>}
      {memberDeclared && <MemberSection item={mine} reload={load} page={target.settingsPage} onClose={onClose} />}
      {owners.map((o) => {
        const view = views[o];
        return view ? (
          <ScopeSection key={o} owner={o} view={view} onSaved={(v) => setViews((cur) => ({ ...cur, [o]: v }))} target={ref} onClose={onClose} />
        ) : null;
      })}
      {othersMissing.length > 0 && (
        <p className="text-sm text-[var(--color-warning)]">
          Waiting for an admin to set: {settingLabels(othersMissing, declared).join(", ")}.
        </p>
      )}
    </div>
  );
}

// MemberSection is the viewer's own settings, kept on this computer: the
// one form or the component's own page, and the sign-in of a tool that
// takes one. In a browser there is nowhere to keep them.
function MemberSection({
  item,
  reload,
  page,
  onClose,
}: {
  readonly item: DesktopComponentSettings | null | undefined;
  readonly reload: () => Promise<void>;
  readonly page?: ComponentSettingsPage;
  readonly onClose: () => void;
}) {
  if (item === undefined) return <p className="text-sm text-[var(--color-fg-subtle)]">Loading your settings…</p>;
  if (item === null) {
    return (
      <p className="text-sm text-[var(--color-fg-subtle)]">
        Your own settings for this component are kept on your computer: open this page in the bnbloc desktop app to set them.
      </p>
    );
  }
  // The page reads and saves through this computer's own store, as the form.
  const own = async () => {
    const fresh = (await desktop.componentSettings())?.find((i) => i.kind === item.kind && i.name === item.name);
    if (!fresh) throw new Error(`${item.name} is not installed on this computer`);
    return memberPageView(fresh);
  };
  const calls: SettingsPageScope = {
    load: own,
    save: async (update) => {
      await desktop.saveComponentSettings(item.kind, item.name, update);
      await reload();
      return own();
    },
  };
  return (
    <section aria-label="Your settings" className="flex flex-col gap-2">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-muted)]">{scopeHeadings.member}</h3>
      {item.auth === "oauth" && <SignInCard item={item} reload={reload} />}
      {page && ofScope(item.fields, "member").length > 0 ? (
        <SettingsPageFrame
          key={page.bundle.sha256}
          name={item.name}
          scope="member"
          scopeLabel={scopeHeadings.member}
          page={{ entry: page.entry, bundleSha256: page.bundle.sha256 }}
          calls={calls}
          onClose={onClose}
        />
      ) : (item.auth !== "oauth" || ofScope(item.fields, "member").length > 0) && (
        <SettingsForm
          title={item.name}
          version={item.version}
          status={statusLine(item)}
          fields={ofScope(item.fields, "member")}
          others={item.fields.filter((f) => f.scope !== "member")}
          missing={item.missing}
          declaration={item.fields}
          onSave={async (update) => {
            await desktop.saveComponentSettings(item.kind, item.name, update);
            await reload();
          }}
          savedText={item.kind === "plugin" ? `${item.name} saved on this computer and restarted` : `${item.name} settings saved on this computer`}
        >
          {(item.tools ?? []).length > 0 && <p className="text-xs text-[var(--color-fg-subtle)]">Tools: {(item.tools ?? []).map((x) => x.name).join(", ")}</p>}
        </SettingsForm>
      )}
    </section>
  );
}

// The heading of each scope's section (and of its settings page).
const scopeHeadings: Record<ComponentSettingScope, string> = {
  member: "Yours, on this computer",
  org: "Your organization's (for every member)",
  platform: "The platform's (for every organization)",
};

// ScopeSection is the organization's or the platform's settings, stored
// sealed on the server.
function ScopeSection({
  owner,
  view,
  target,
  onSaved,
  onClose,
}: {
  readonly owner: SettingsOwner;
  readonly view: ComponentSettingsView;
  readonly target: { readonly kind: string; readonly name: string };
  readonly onSaved: (view: ComponentSettingsView) => void;
  readonly onClose: () => void;
}) {
  const updated = view.updatedBy || view.updatedAtMs ? `Saved${view.updatedBy ? ` by ${view.updatedBy}` : ""}${view.updatedAtMs ? ` ${relativeTime(view.updatedAtMs)}` : ""}.` : "Not saved yet.";
  // The page reads and saves through the scope's routes, as the form.
  const calls: SettingsPageScope = {
    load: async () => scopePageView(await componentSettingsApi.get(owner, target)),
    save: async (update) => {
      const saved = await componentSettingsApi.save(owner, target, update);
      onSaved(saved);
      return scopePageView(saved);
    },
  };
  return (
    <section aria-label={owner === "org" ? "Organization settings" : "Platform settings"} className="flex flex-col gap-2">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-muted)]">{scopeHeadings[view.scope]}</h3>
      {!view.available ? (
        <p className="text-sm text-[var(--color-fg-subtle)]">Settings are off on this deployment: the gateway has no secret store key (BNBLOC_SECRET_STORE_KEY).</p>
      ) : view.page ? (
        <SettingsPageFrame
          key={view.page.bundleSha256}
          name={view.name}
          scope={view.scope}
          scopeLabel={scopeHeadings[view.scope]}
          page={view.page}
          calls={calls}
          onClose={onClose}
        />
      ) : (
        <SettingsForm
          title={view.name}
          version={view.version}
          status={updated}
          fields={view.fields}
          missing={view.missing.map((s) => s.key)}
          declaration={[...view.fields, ...view.missing]}
          reachesMembers={view.reachesMembers}
          asks={view.asks}
          onSave={async (update) => onSaved(await componentSettingsApi.save(owner, target, update))}
          savedText={`${view.name} settings saved`}
        />
      )}
    </section>
  );
}

// SignInCard is an OAuth tool: sign in (the tool's sign-in page opens in
// the browser) or sign out of it on this computer.
function SignInCard({ item, reload }: { readonly item: DesktopComponentSettings; readonly reload: () => Promise<void> }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const run = async (fn: () => Promise<void>, done: string) => {
    setBusy(true);
    try {
      await fn();
      toast.success(done);
      await reload();
    } catch (e) {
      toast.error(errorText(e, "Something went wrong"));
    } finally {
      setBusy(false);
    }
  };
  const signedIn = !!item.signIn?.valid;
  const setup = needsSetupText(item.missing, item.fields);
  return (
    <Card className="flex flex-col gap-3 px-4 py-3">
      <div className="min-w-0">
        <span className="block text-xs text-[var(--color-fg-muted)]">{signInLine(item)} Used only for runs you ask for yourself.</span>
        {setup && <span className="block text-xs text-[var(--color-warning)]">{setup}</span>}
        {busy && !signedIn && <span className="block text-xs text-[var(--color-fg-subtle)]">Finish signing in in your browser…</span>}
      </div>
      <div className="flex gap-2">
        <Button size="sm" variant={signedIn ? "ghost" : "primary"} disabled={busy} onClick={() => void run(() => desktop.signInMcpTool(item.name), `Signed in to ${item.name}`)}>
          {signedIn ? "Sign in again" : "Sign in"}
        </Button>
        {item.signIn && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => void run(() => desktop.signOutMcpTool(item.name), `Signed out of ${item.name} on this computer`)}>
            Sign out
          </Button>
        )}
      </div>
    </Card>
  );
}

// statusLine is one line: what the component is and, for a stdio tool, its
// download on this computer; a plugin's state on this computer.
function statusLine(c: DesktopComponentSettings): string {
  if (c.kind === "plugin") return `A plugin of this desktop app. ${c.message || c.state}`.trim();
  if (c.transport !== "stdio") return "An MCP tool at its service URL.";
  const local = c.state === "ready" ? "Downloaded and verified." : c.state === "failed" ? `Download failed: ${c.message}` : c.message || "Downloading…";
  return `An MCP tool on this computer. ${local}`;
}
