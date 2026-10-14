import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowCircleUpIcon } from "@phosphor-icons/react";
import { desktop, type DesktopUpdateCheck, type DesktopUpdateProgress } from "@/lib/desktop";
import { cn } from "@/lib/cn";
import { useToast } from "@/ui/Toast";
import { DesktopUpdateDialog } from "@/views/DesktopUpdateDialog";
import { enterUpdateWork, progressLabel } from "@/views/desktopUpdateState";
import { updateCheckIntervalMs } from "@/views/updateConfigFields";
import { errorText } from "@/lib/errors";

export interface DesktopUpdateController {
  // Rendered by the sidebar footer: a quiet entry the user clicks when ready.
  readonly renderNotice: (collapsed: boolean) => ReactNode;
  // The install dialog; open only after the user clicks the entry, or
  // immediately for a forced (below-minimum) update.
  readonly dialog: ReactNode;
}

// useDesktopUpdate runs the background update check for the desktop shell.
// An optional update never interrupts: no dialog, toast or OS notification —
// only the sidebar entry. A forced update still opens the blocking dialog.
export function useDesktopUpdate(signedIn: boolean): DesktopUpdateController {
  const toast = useToast();
  const [update, setUpdate] = useState<DesktopUpdateCheck | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [progress, setProgress] = useState<DesktopUpdateProgress | null>(null);
  const inFlight = useRef(false);

  const install = useCallback(async () => {
    const release = enterUpdateWork();
    if (!release) return;
    setInstalling(true);
    setProgress(null);
    try {
      const ok = await desktop.installUpdate();
      if (!ok) {
        setUpdate(null);
        setDialogOpen(false);
      }
    } catch (e: unknown) {
      toast.error(errorText(e, "Install failed"));
    } finally {
      release();
      setInstalling(false);
    }
  }, [toast]);

  useEffect(() => {
    if (!signedIn || !desktop.available()) return;
    let unlisten: () => void = () => undefined;
    void desktop.onUpdateProgress((next) => setProgress(next)).then((fn) => {
      unlisten = fn;
    });
    return () => unlisten();
  }, [signedIn]);

  useEffect(() => {
    if (!signedIn || !desktop.available()) return;
    let cancelled = false;
    let timer: number | null = null;

    const run = async (autoInstall: boolean) => {
      if (cancelled || inFlight.current) return;
      const release = enterUpdateWork();
      if (!release) return;
      inFlight.current = true;
      try {
        const result = await desktop.checkUpdate();
        if (cancelled || !result?.available) return;
        // An automatic install restarts the app and with it the bridge:
        // never while it drives agent turns (live turns PRD) — offer the
        // update instead; the next check installs it once idle.
        if (autoInstall && (await desktop.bridgeRunningTurns()) === 0) {
          setInstalling(true);
          await desktop.installUpdate();
          return;
        }
        setUpdate(result);
        if (result.forced) setDialogOpen(true);
      } catch (e: unknown) {
        // Background work never interrupts (unobtrusive-update PRD): a failed
        // automatic check is logged and retried on the next interval.
        console.warn("bnbloc: automatic update check failed:", errorText(e, "unknown error"));
      } finally {
        release();
        inFlight.current = false;
        setInstalling(false);
      }
    };

    const boot = async () => {
      try {
        const policy = await desktop.updatePolicy();
        if (cancelled || !policy?.enabled || !policy.autoCheck) return;
        void run(policy.autoInstall);
        // Clamped: an interval past the 32-bit timer limit would overflow
        // into an immediate re-fire and a tight check loop.
        timer = window.setInterval(() => void run(policy.autoInstall), updateCheckIntervalMs(policy.checkIntervalHours));
      } catch (e: unknown) {
        console.warn("bnbloc: could not load the update policy:", errorText(e, "unknown error"));
      }
    };

    void boot();
    return () => {
      cancelled = true;
      if (timer !== null) window.clearInterval(timer);
    };
  }, [signedIn]);

  const renderNotice = useCallback(
    (collapsed: boolean) =>
      update ? <DesktopUpdateNotice version={update.version} collapsed={collapsed} onOpen={() => setDialogOpen(true)} /> : null,
    [update],
  );

  const dialog =
    update && dialogOpen ? (
      <DesktopUpdateDialog
        update={update}
        progress={progressLabel(progress)}
        installing={installing}
        onClose={update.forced ? () => undefined : () => setDialogOpen(false)}
        onInstall={install}
      />
    ) : null;

  return { renderNotice, dialog };
}

function DesktopUpdateNotice({
  version,
  collapsed,
  onOpen,
}: {
  readonly version: string | null | undefined;
  readonly collapsed: boolean;
  readonly onOpen: () => void;
}) {
  const label = version ? `Update ${version} available` : "Update available";
  return (
    <button
      type="button"
      onClick={onOpen}
      title={label}
      aria-label={label}
      className={cn(
        "tactile relative flex items-center rounded-[var(--radius)] text-sm text-[var(--color-accent)] hover:bg-[var(--color-accent-soft)]",
        collapsed ? "justify-center p-2" : "gap-2 px-3 py-2",
      )}
    >
      <ArrowCircleUpIcon size={18} weight="fill" className="shrink-0" />
      {collapsed ? (
        <span className="absolute right-1.5 top-1.5 size-2 rounded-full bg-[var(--color-accent)]" />
      ) : (
        <span className="min-w-0 flex-1 truncate text-left">{label}</span>
      )}
    </button>
  );
}
