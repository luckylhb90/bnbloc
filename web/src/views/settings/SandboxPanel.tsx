import { useCallback, useEffect, useState } from "react";
import { useTenantPolicy } from "@/lib/tenantPolicy";
import { desktop } from "@/lib/desktop";
import { canRestartVm, privateHostsLine, statusLine, type SandboxStatus } from "@/lib/sandboxStatus";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";
import { useVisiblePoll } from "@/lib/useVisiblePoll";

const TONE_CLASS = {
  muted: "text-[var(--color-fg-subtle)]",
  busy: "text-[var(--color-fg-muted)]",
  ok: "text-[var(--color-fg)]",
  error: "text-[var(--color-danger)]",
} as const;

// SandboxPanel is the agent sandbox on this computer, in its Components
// settings dialog above the form that sets what it does (the mode, the
// environment, the private hosts are the component's own settings): the
// VM's state, the private hosts open now, Restart VM. `version` changes when the member saves their
// settings, so the panel reads the state again at once.
export function SandboxPanel({ version }: { readonly version: number }) {
  const toast = useToast();
  const [status, setStatus] = useState<SandboxStatus | null>(null);
  const [busy, setBusy] = useState<"restart" | "hosts" | null>(null);
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

  // Poll faster while the VM or the private hosts are coming up.
  const hostsState = status?.privateHosts.state;
  const settling = status?.state === "starting" || status?.state === "installing" || hostsState === "applying" || hostsState === "waiting";
  useEffect(() => {
    void load();
  }, [load, version]);
  useVisiblePoll(() => void load(), settling ? 2000 : 10000, true);

  const run = async (kind: NonNullable<typeof busy>, fn: () => Promise<SandboxStatus | null>, done: string) => {
    setBusy(kind);
    try {
      const next = await fn();
      if (next) setStatus(next);
      toast.success(done);
    } catch (e) {
      toast.error(errorText(e, "Something went wrong"));
      void load();
    } finally {
      setBusy(null);
    }
  };

  const line = status ? statusLine(status) : null;
  const hosts = status?.privateHosts;
  const hostsLine = hosts ? privateHostsLine(hosts) : null;

  return (
    <Card className="flex flex-col gap-3 px-4 py-3">
      <p className="text-xs text-[var(--color-fg-muted)]">
        Commands your agents run execute inside a local Linux VM; your agents, skills and logins stay on this computer. The VM reaches the public internet but no
        private address other than the hosts open to your own agents below.
      </p>
      {requiredForBorrow && (
        <p className="text-xs text-[var(--color-warning)]">
          Your organization requires the agent sandbox for borrowed agents: while it is off or not ready, other members cannot borrow your agents.
        </p>
      )}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <span className={`min-w-0 text-sm ${line ? TONE_CLASS[line.tone] : TONE_CLASS.muted}`}>{line ? line.text : "Checking the sandbox…"}</span>
        {status && canRestartVm(status) && (
          <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void run("restart", () => desktop.restartSandboxVm(), "VM restarted")}>
            {busy === "restart" ? "Restarting…" : "Restart VM"}
          </Button>
        )}
      </div>
      {hosts && hosts.hosts.length > 0 && (
        <div className="flex flex-col gap-1 border-t border-[var(--color-border)] pt-3 text-xs">
          <span className="font-medium">Private hosts open to your own agents</span>
          <span className="font-mono">
            {hosts.hosts.map((h) => `${h}${hosts.applied.includes(h) ? "" : " (not open yet)"}`).join(", ")}
          </span>
          {hostsLine && (
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span className={`min-w-0 ${TONE_CLASS[hostsLine.tone]}`}>{hostsLine.text}</span>
              {(hosts.state === "ready" || hosts.state === "error") && (
                <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => void run("hosts", () => desktop.applySandboxPrivateHosts(), "Applying the private hosts again")}>
                  Apply again
                </Button>
              )}
            </div>
          )}
        </div>
      )}
      {status?.installed && status.name && (
        <span className="text-xs text-[var(--color-fg-subtle)]">
          {status.name} {status.version}
          {status.isolation ? ` · ${status.isolation}` : ""}
        </span>
      )}
    </Card>
  );
}
