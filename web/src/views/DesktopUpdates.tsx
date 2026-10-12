import { useCallback, useEffect, useState } from "react";
import { DownloadSimpleIcon, ArrowClockwiseIcon } from "@phosphor-icons/react";
import { desktop, type AppInfo, type DesktopUpdateCheck, type DesktopUpdateProgress } from "@/lib/desktop";
import { cn } from "@/lib/cn";
import { Badge, Card } from "@/ui/Card";
import { Button } from "@/ui/Button";
import { useToast } from "@/ui/Toast";
import { DesktopUpdateDialog } from "@/views/DesktopUpdateDialog";
import { enterUpdateWork, progressLabel } from "@/views/desktopUpdateState";
import { errorText } from "@/lib/errors";

type CheckState =
  | { readonly kind: "idle" }
  | { readonly kind: "checking" }
  | { readonly kind: "upToDate" }
  | { readonly kind: "available"; readonly update: DesktopUpdateCheck }
  | { readonly kind: "failed"; readonly message: string };

export function DesktopUpdatePanel() {
  const toast = useToast();
  const [appInfo, setAppInfo] = useState<AppInfo | null>(null);
  const [policyEnabled, setPolicyEnabled] = useState(false);
  const [policyLoaded, setPolicyLoaded] = useState(false);
  const [state, setState] = useState<CheckState>({ kind: "idle" });
  const [installing, setInstalling] = useState(false);
  const [progress, setProgress] = useState<DesktopUpdateProgress | null>(null);

  useEffect(() => {
    if (!desktop.available()) return;
    let mounted = true;
    void desktop.appInfo().then((info) => {
      if (mounted) setAppInfo(info);
    }).catch((e: unknown) => {
      if (mounted) setState({ kind: "failed", message: errorText(e, "Could not load app version") });
    });
    void desktop.updatePolicy()
      .then((policy) => {
        if (!mounted) return;
        setPolicyEnabled(policy?.enabled === true);
        setPolicyLoaded(true);
      })
      .catch((e: unknown) => {
        if (!mounted) return;
        setPolicyLoaded(true);
        setState({ kind: "failed", message: errorText(e, "Could not load update policy") });
      });
    return () => {
      mounted = false;
    };
  }, []);

  useEffect(() => {
    if (!desktop.available()) return;
    let unlisten: () => void = () => undefined;
    void desktop.onUpdateProgress((next) => setProgress(next)).then((fn) => {
      unlisten = fn;
    });
    return () => unlisten();
  }, []);

  const check = useCallback(async () => {
    const release = enterUpdateWork();
    if (!release) {
      setState({ kind: "failed", message: "An update check is already running." });
      return;
    }
    setState({ kind: "checking" });
    setProgress(null);
    try {
      const result = await desktop.checkUpdate();
      if (!result) setState({ kind: "failed", message: "Desktop updater is unavailable in this window." });
      else if (result.available) setState({ kind: "available", update: result });
      else setState({ kind: "upToDate" });
    } catch (e: unknown) {
      setState({ kind: "failed", message: errorText(e, "Update check failed") });
    } finally {
      release();
    }
  }, []);

  const install = useCallback(async () => {
    const release = enterUpdateWork();
    if (!release) return;
    setInstalling(true);
    setProgress(null);
    try {
      const ok = await desktop.installUpdate();
      if (!ok) setState({ kind: "upToDate" });
    } catch (e: unknown) {
      const message = errorText(e, "Install failed");
      setState({ kind: "failed", message });
      toast.error(message);
    } finally {
      release();
      setInstalling(false);
    }
  }, [toast]);

  if (!appInfo) return null;
  const latest = state.kind === "available" ? state.update : null;
  const progressText = progressLabel(progress);

  return (
    <>
      <Card className="p-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">Desktop app</span>
              <Badge tone="neutral">v{appInfo.version}</Badge>
              <Badge tone="neutral">{appInfo.platform}</Badge>
            </div>
            <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">
              Updates are managed by this desktop shell and the signed gateway policy.
            </p>
          </div>
          {policyLoaded && policyEnabled ? (
            <Button size="sm" variant="outline" onClick={() => void check()} disabled={state.kind === "checking" || installing}>
              <ArrowClockwiseIcon size={14} />
              {state.kind === "checking" ? "Checking…" : "Check for updates"}
            </Button>
          ) : (
            <Badge tone="neutral">Updates disabled</Badge>
          )}
        </div>
        {state.kind === "upToDate" && <p className="mt-3 text-sm text-[var(--color-accent)]">You are up to date.</p>}
        {state.kind === "failed" && <p className="mt-3 text-sm text-[var(--color-danger)]">{state.message}</p>}
        {latest && (
          <div className={cn("mt-3 rounded-[var(--radius)] border p-3 text-sm", latest.forced ? "border-[var(--color-danger)]/40 bg-[var(--color-danger)]/10" : "border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)]")}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <span className="font-medium">Version {latest.version} is available</span>
                {latest.forced && <Badge tone="danger" className="ml-2">Required</Badge>}
              </div>
              <Button size="sm" variant={latest.forced ? "danger" : "primary"} onClick={() => void install()} disabled={installing}>
                <DownloadSimpleIcon size={14} />
                {installing ? "Installing…" : latest.forced ? "Install required update" : "Install"}
              </Button>
            </div>
            {latest.notes && <p className="mt-2 whitespace-pre-wrap text-[var(--color-fg-muted)]">{latest.notes}</p>}
            {progressText && <p className="mt-2 text-xs tabular text-[var(--color-fg-subtle)]">{progressText}</p>}
          </div>
        )}
      </Card>
      {latest?.forced && <DesktopUpdateDialog update={latest} progress={progressText} installing={installing} onClose={() => undefined} onInstall={install} />}
    </>
  );
}
