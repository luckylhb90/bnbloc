import { useCallback, useEffect, useState } from "react";
import { useTenantPolicy } from "@/lib/tenantPolicy";
import { desktop } from "@/lib/desktop";
import {
  canRestartVm,
  SANDBOX_MODES,
  shellEnvReloadText,
  statusLine,
  type SandboxMode,
  type SandboxStatus,
} from "@/lib/sandboxStatus";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { Input } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";
import { useVisiblePoll } from "@/lib/useVisiblePoll";

const TONE_CLASS = {
  muted: "text-[var(--color-fg-subtle)]",
  busy: "text-[var(--color-fg-muted)]",
  ok: "text-[var(--color-fg)]",
  error: "text-[var(--color-danger)]",
} as const;

// AgentSandbox lets the member choose which of their agent runs execute
// their commands in the local sandbox VM, and shows the VM's state. The VM
// runtime is a component the desktop app supervises, so the section exists
// only in the desktop shell; in a browser it renders nothing.
export function AgentSandbox() {
  const inShell = desktop.available();
  const toast = useToast();
  const [status, setStatus] = useState<SandboxStatus | null>(null);
  const [busy, setBusy] = useState<"mode" | "restart" | "env" | "shell" | null>(null);
  // The env names being edited; null = showing the saved list.
  const [envDraft, setEnvDraft] = useState<string | null>(null);
  // The organization's policy: when it requires the sandbox for borrowing,
  // this member's agents cannot be borrowed while the sandbox is off/not ready.
  const requiredForBorrow = useTenantPolicy()?.requireSandboxForBorrow ?? false;

  const load = useCallback(async () => {
    try {
      setStatus(await desktop.sandboxStatus());
    } catch {
      // background refresh: the next poll retries
    }
  }, []);

  // Poll faster while the VM is coming up.
  const settling = status?.state === "starting" || status?.state === "installing";
  useEffect(() => {
    if (inShell) void load();
  }, [inShell, load]);
  useVisiblePoll(() => void load(), settling ? 2000 : 10000, inShell);

  if (!inShell) return null;

  const run = async (kind: NonNullable<typeof busy>, fn: () => Promise<SandboxStatus | null>, done: string): Promise<boolean> => {
    setBusy(kind);
    try {
      const next = await fn();
      if (next) setStatus(next);
      toast.success(done);
      return true;
    } catch (e) {
      toast.error(errorText(e, "Something went wrong"));
      void load();
      return false;
    } finally {
      setBusy(null);
    }
  };

  const setMode = (mode: SandboxMode) => {
    if (!status || mode === status.mode || busy) return;
    void run("mode", () => desktop.setSandboxMode(mode), "Agent sandbox setting saved");
  };

  const savedEnv = (status?.env ?? []).join(", ");
  const envText = envDraft ?? savedEnv;
  const saveEnv = () => {
    if (busy || envDraft === null) return;
    const names = envDraft.split(/[\s,]+/).filter((n) => n !== "");
    void run("env", () => desktop.setSandboxEnv(names), "Sandbox environment saved").then((ok) => {
      if (ok) setEnvDraft(null);
    });
  };

  // Read the login shell's environment again; the bridge and the agents
  // restart with it.
  const reloadShellEnv = async () => {
    if (busy) return;
    setBusy("shell");
    try {
      const r = await desktop.reloadShellEnv();
      if (r) {
        const { ok, text } = shellEnvReloadText(r);
        if (ok) toast.success(text);
        else toast.error(text);
      }
    } catch (e) {
      toast.error(errorText(e, "Something went wrong"));
    } finally {
      setBusy(null);
      void load();
    }
  };

  const line = status ? statusLine(status) : null;
  const mode = status?.mode ?? "borrowed";

  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold">Agent sandbox</h2>
        <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
          Commands your agents run execute inside a local Linux VM; your agents, skills and logins stay on this computer.
        </p>
      </div>
      <Card className="flex flex-col gap-3 px-4 py-3">
        <Segment options={SANDBOX_MODES} value={mode} onChange={setMode} />
        <p className="text-xs text-[var(--color-fg-muted)]">{SANDBOX_MODES.find((m) => m.value === mode)?.hint}</p>
        {requiredForBorrow && (
          <p className="text-xs text-[var(--color-warning)]">
            Your organization requires the agent sandbox for borrowed agents: while it is off or not ready, other
            members cannot borrow your agents.
          </p>
        )}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <span className={`min-w-0 text-sm ${line ? TONE_CLASS[line.tone] : TONE_CLASS.muted}`}>
            {line ? line.text : "Checking the sandbox…"}
            {status?.state === "notInstalled" && (
              <>
                {" "}
                <a className="underline" href="#/components">
                  Open Components
                </a>
              </>
            )}
          </span>
          {status && canRestartVm(status) && (
            <Button
              size="sm"
              variant="ghost"
              disabled={busy !== null}
              onClick={() => void run("restart", () => desktop.restartSandboxVm(), "VM restarted")}
            >
              {busy === "restart" ? "Restarting…" : "Restart VM"}
            </Button>
          )}
        </div>
        <div className="flex flex-col gap-1.5 border-t border-[var(--color-border)] pt-3">
          <label htmlFor="sandbox-env" className="text-sm font-medium">
            Environment passed to sandboxed agents
          </label>
          <p className="text-xs text-[var(--color-fg-muted)]">
            Commands in the VM get only your terminal, locale and git identity settings. List the environment variables
            your own agents need there, such as an API key: names like EXAMPLE_API_KEY, or a prefix like EXAMPLE_*. Only
            the names are saved; each value is read from your agent's environment when it runs. Runs of your agents by
            other members never get them.
          </p>
          <div className="flex items-center gap-2">
            <Input
              id="sandbox-env"
              className="font-mono"
              placeholder="EXAMPLE_API_KEY, EXAMPLE_BASE_URL"
              value={envText}
              spellCheck={false}
              autoCapitalize="characters"
              onChange={(e) => setEnvDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") saveEnv();
              }}
            />
            <Button size="sm" disabled={busy !== null || envDraft === null || envDraft === savedEnv} onClick={saveEnv}>
              {busy === "env" ? "Saving…" : "Save"}
            </Button>
          </div>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3 border-t border-[var(--color-border)] pt-3">
          <p className="min-w-0 flex-1 text-xs text-[var(--color-fg-muted)]">
            Your agents get the variables your login shell exports (such as in ~/.zshrc or ~/.zprofile), read when the
            app starts. After exporting a new one, reload it here: the bridge and your agents restart.
          </p>
          <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void reloadShellEnv()}>
            {busy === "shell" ? "Reloading…" : "Reload shell environment"}
          </Button>
        </div>
        {status?.installed && status.name && (
          <span className="text-xs text-[var(--color-fg-subtle)]">
            {status.name} {status.version}
            {status.isolation ? ` · ${status.isolation}` : ""}
          </span>
        )}
      </Card>
    </section>
  );
}
