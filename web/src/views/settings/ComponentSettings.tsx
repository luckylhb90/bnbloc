import { useCallback, useEffect, useState } from "react";
import { desktop } from "@/lib/desktop";
import { signInLine } from "@/lib/mcpTools";
import { needsSetupText, type DesktopComponentSettings } from "@/lib/componentSettings";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { SettingsForm } from "@/views/settings/SettingsForm";
import { errorText } from "@/lib/errors";

// ComponentSettings (Settings → Components) is where a member sets up the
// components that run on their computer: the MCP tools their agents use and
// the plugins the desktop runs (a personal bot among them). Each shows its
// settings in the one form: the member's own are stored on this computer
// (0600 in the app's config dir), never sent to the server, and used only
// for runs they ask for themselves; the organization's and the platform's
// are shown as set or not, never their values. A tool that takes a sign-in
// is signed in to here, in the browser. A component whose required settings
// are not all set needs setup: it does not run and agents are not offered
// it. In a browser there is nothing to set.
export function ComponentSettings() {
  const inShell = desktop.available();
  const [items, setItems] = useState<DesktopComponentSettings[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setItems((await desktop.componentSettings()) ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load component settings"));
      setItems([]);
    }
  }, []);

  useEffect(() => {
    if (inShell) void load();
  }, [inShell, load]);

  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold">Components</h2>
        <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
          Settings of the tools and plugins that run on this computer. Yours stay here and are used only for runs you ask for yourself.
        </p>
      </div>
      {!inShell && <p className="text-sm text-[var(--color-fg-subtle)]">Component settings and sign-ins live in the bnbloc desktop app. Open Settings → Components there.</p>}
      {inShell && error && <ErrorBox>{error}</ErrorBox>}
      {inShell && items && items.length === 0 && !error && (
        <p className="text-sm text-[var(--color-fg-subtle)]">No tool or plugin with settings is installed for you. Tools are listed in Library → Tools once your platform admin publishes them.</p>
      )}
      {(items ?? []).map((c) => (
        <div key={`${c.kind}/${c.name}`} className="flex flex-col gap-2">
          {c.auth === "oauth" && <SignInCard item={c} reload={load} />}
          {(c.auth !== "oauth" || c.fields.length > 0) && (
            <SettingsForm
              title={c.name}
              version={c.version}
              status={statusLine(c)}
              fields={c.fields.filter((f) => f.scope === "member")}
              others={c.fields.filter((f) => f.scope !== "member")}
              missing={c.missing}
              declaration={c.fields}
              onSave={async (update) => {
                await desktop.saveComponentSettings(c.kind, c.name, update);
                await load();
              }}
              savedText={c.kind === "plugin" ? `${c.name} saved on this computer and restarted` : `${c.name} settings saved on this computer`}
            >
              {(c.tools ?? []).length > 0 && <p className="text-xs text-[var(--color-fg-subtle)]">Tools: {(c.tools ?? []).map((x) => x.name).join(", ")}</p>}
            </SettingsForm>
          )}
        </div>
      ))}
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
        <span className="block text-sm font-medium">
          {item.name} <span className="text-xs font-normal text-[var(--color-fg-subtle)]">{item.version}</span>
        </span>
        <span className="block text-xs text-[var(--color-fg-muted)]">{signInLine(item)} Used only for runs you ask for yourself.</span>
        {setup && <span className="block text-xs text-[var(--color-warning)]">{setup}</span>}
        {busy && !signedIn && <span className="block text-xs text-[var(--color-fg-subtle)]">Finish signing in in your browser…</span>}
      </div>
      {(item.tools ?? []).length > 0 && <p className="text-xs text-[var(--color-fg-subtle)]">Tools: {(item.tools ?? []).map((x) => x.name).join(", ")}</p>}
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
