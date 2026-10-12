import { useState } from "react";
import { Select } from "@/ui/Select";
import { DownloadSimpleIcon } from "@phosphor-icons/react";
import { api, type AgentTarget } from "@/lib/api";
import { useDesktopDownload } from "@/lib/useDesktopDownload";
import { useLocalAgent } from "@/lib/useLocalAgent";
import { Button } from "@/ui/Button";
import { Input, Textarea, Field } from "@/ui/Input";
import { Dialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";
import { ReleaseDownloadButton } from "@/views/ReleaseDownloadButton";
import { PlatformAgentsHint } from "@/views/PlatformAgentsHint";
import { errorText } from "@/lib/errors";

// RunAgentDialog discovers the caller's local agent core, lists its targets, and
// starts a session that adopts a room. Mirrors the old runLocalAgent flow with a
// proper form + in-app feedback instead of native prompts.
export function RunAgentDialog({
  open,
  onClose,
  onStarted,
}: {
  open: boolean;
  onClose: () => void;
  onStarted: (roomId: string) => void;
}) {
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const desktopDownload = useDesktopDownload();
  // Reuse the shared hook so discovery, /v1/cores availability, targets, and the
  // manual address all come from ONE place — this is also what fixes the old
  // dead-end where typing a manual URL never re-enabled the button.
  const agent = useLocalAgent(open);
  const {
    targets,
    target,
    setTarget,
    url: localUrl,
    setUrl: setLocalUrl,
    token: localToken,
    setToken: setLocalToken,
    detected,
    coreStatus,
    devDiscovery,
    save,
  } = agent;
  const launchTarget = targets.find((t) => t.id === target) ?? targets[0];
  // Why Run is unavailable, stated next to the button — never a silent
  // disabled control.
  const blocked =
    detected === null
      ? "Checking for agent cores…"
      : detected === false
        ? "No agent core is connected (see above)."
        : targets.length === 0
          ? "No agent here can be started from this page. Agents run by your desktop app are started from a room: message them there."
          : null;

  const run = async () => {
    if (!launchTarget) return;
    setBusy(true);
    try {
      const r = await api.runLocalAgent({
        prompt: prompt.trim() || "Hello from a local agent session",
        agentTargetId: launchTarget.id,
        // Gateway splits the core from the qualified id; sending core is
        // redundant but explicit.
        core: launchTarget.core || undefined,
        ...agent.devAddress,
      });
      setPrompt("");
      onStarted(r.roomId);
    } catch (e) {
      toast.error(errorText(e, "Could not start the local agent"));
    } finally {
      setBusy(false);
    }
  };

  // Group targets by their core so different local runtimes are visually
  // separated in the <Select>. Preserves first-seen core order; a target with
  // no core (legacy) is grouped under "other".
  const groupedTargets: { core: string; items: AgentTarget[] }[] = [];
  for (const t of targets) {
    const core = t.core || "other";
    let g = groupedTargets.find((x) => x.core === core);
    if (!g) {
      g = { core, items: [] };
      groupedTargets.push(g);
    }
    g.items.push(t);
  }

  // Installed core names from the gateway (GET /v1/cores), never a baked-in list.
  const knownCoreNames = coreStatus.map((c) => c.name).join(", ");

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Run a local agent"
      description="Starts a real session on your local agent core and opens it as a room."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={() => void run()}
            disabled={busy || blocked !== null}
            title={blocked ?? undefined}
          >
            {busy ? "Starting…" : "Run agent"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {detected === false && (
          <div className="rounded-[var(--radius)] border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 p-3 text-sm text-[var(--color-fg-muted)]">
            <p className="font-medium text-[var(--color-fg)]">
              No agent core connected
            </p>
            <p className="mt-1">
              {knownCoreNames
                ? `Installed cores on this workspace: ${knownCoreNames}. None of them is reachable for you right now.`
                : "No agent core is installed on this workspace yet."}{" "}
              Cores are installed by a platform admin (Platform → Components, or{" "}
              <span className="font-mono">bnbloc cores install</span>); ask yours if you need one. The
              bnbloc desktop app connects your machine&apos;s agents once a core is installed.
            </p>
            <p className="mt-2">
              You can still collaborate with people, or borrow another member&apos;s agent, without a
              core of your own.
            </p>
            <PlatformAgentsHint className="mt-2" />
            <div className="mt-3">
              {desktopDownload.file ? (
                <ReleaseDownloadButton file={desktopDownload.file} label="Download desktop app" variant="outline" />
              ) : (
                <Button variant="outline" size="sm" disabled>
                  <DownloadSimpleIcon size={14} />
                  {desktopDownload.loading ? "Finding download…" : "Download not configured"}
                </Button>
              )}
              {!desktopDownload.file && !desktopDownload.loading && (
                <p className="mt-2 text-xs text-[var(--color-fg-subtle)]">
                  Download not configured — ask your admin.
                </p>
              )}
            </div>
          </div>
        )}

        <Field label="Prompt" hint="What should the agent start working on?">
          <Textarea
            rows={3}
            autoFocus
            placeholder="Summarize the open issues and propose a plan"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
          />
        </Field>

        {targets.length > 0 && (
          <Field label="Agent target">
            <Select
              value={launchTarget?.id || ""}
              onChange={(e) => setTarget(e.target.value)}
              className="h-10 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm text-[var(--color-fg)] focus:border-[var(--color-accent)] focus:outline-none"
            >
              {groupedTargets.map((g) => (
                <optgroup key={g.core} label={g.core}>
                  {g.items.map((t) => (
                    <option key={t.id} value={t.id}>
                      {t.provider} ({t.id})
                    </option>
                  ))}
                </optgroup>
              ))}
            </Select>
          </Field>
        )}

        {blocked && detected !== false && (
          <p className="text-xs text-[var(--color-fg-muted)]" role="status">
            {blocked}
          </p>
        )}

        {devDiscovery && (
        <details className="text-sm">
          <summary className="cursor-pointer text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]">
            Advanced — local core address (dev gateway)
          </summary>
          <div className="mt-3 flex flex-col gap-3">
            <Field label="Local agent URL">
              <Input
                placeholder="http://127.0.0.1:4545"
                value={localUrl}
                onChange={(e) => setLocalUrl(e.target.value)}
              />
            </Field>
            <Field label="Local agent token">
              <Input
                type="password"
                placeholder="token"
                value={localToken}
                onChange={(e) => setLocalToken(e.target.value)}
              />
            </Field>
            <div>
              <Button variant="outline" size="sm" onClick={save}>
                Save &amp; re-check
              </Button>
            </div>
          </div>
        </details>
        )}
      </div>
    </Dialog>
  );
}
