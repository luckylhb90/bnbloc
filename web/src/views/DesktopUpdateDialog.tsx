import { DownloadSimpleIcon, WarningCircleIcon } from "@phosphor-icons/react";
import type { DesktopUpdateCheck } from "@/lib/desktop";
import { cn } from "@/lib/cn";
import { Badge } from "@/ui/Card";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { RunningTurnsWarning } from "@/views/RunningTurnsWarning";

export function DesktopUpdateDialog({
  update,
  progress,
  installing,
  onClose,
  onInstall,
}: {
  readonly update: DesktopUpdateCheck;
  readonly progress: string | null;
  readonly installing: boolean;
  readonly onClose: () => void;
  readonly onInstall: () => void;
}) {
  return (
    <Dialog
      open
      onClose={onClose}
      title={update.forced ? "Required desktop update" : "Desktop update available"}
      description={update.forced ? "This version is below the minimum supported version. Install before continuing." : undefined}
      size="sm"
      dismissible={!update.forced}
      footer={
        <>
          {!update.forced && <Button variant="ghost" onClick={onClose}>Later</Button>}
          <Button variant={update.forced ? "danger" : "primary"} onClick={onInstall} disabled={installing}>
            <DownloadSimpleIcon size={14} />
            {installing ? "Installing…" : update.forced ? "Install required update" : "Install update"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        <div className={cn("rounded-[var(--radius)] border p-3", update.forced ? "border-[var(--color-danger)]/40 bg-[var(--color-danger)]/10" : "border-[var(--color-border)] bg-[var(--color-surface-2)]")}>
          <div className="flex items-center gap-2 font-medium">
            {update.forced && <WarningCircleIcon size={17} weight="fill" className="text-[var(--color-danger)]" />}
            Version {update.version}
            {update.forced && <Badge tone="danger">Required</Badge>}
          </div>
          {update.notes && <p className="mt-2 whitespace-pre-wrap text-[var(--color-fg-muted)]">{update.notes}</p>}
        </div>
        <RunningTurnsWarning />
        {progress && <p className="tabular text-xs text-[var(--color-fg-subtle)]">{progress}</p>}
      </div>
    </Dialog>
  );
}
