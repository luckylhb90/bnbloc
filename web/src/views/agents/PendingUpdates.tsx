import { useState } from "react";
import { desktop } from "@/lib/desktop";
import { errorText } from "@/lib/errors";
import { pendingUpdates, pendingUpdatesNote, runningTurnsWarning } from "@/lib/pendingUpdates";
import type { InternalAdapterStatus } from "@/lib/desktop";
import { Button } from "@/ui/Button";
import { ConfirmDialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";

// The component updates that wait for this machine's agent turns to finish
// (live turns PRD): what waits and for how many tasks, and "Apply now",
// which warns that it stops them. Hidden when nothing waits.
export function PendingUpdates({ statuses }: { readonly statuses: readonly InternalAdapterStatus[] }) {
  const toast = useToast();
  const [confirming, setConfirming] = useState(false);
  const pending = pendingUpdates(statuses);
  if (pending.length === 0) return null;
  const waiting = Math.max(...pending.map((p) => p.waitingFor));
  const apply = async () => {
    try {
      await desktop.applyPendingUpdates();
      toast.success("Applying the update");
    } catch (e) {
      toast.error(errorText(e, "Could not apply the update"));
    }
  };
  return (
    <div className="flex items-start justify-between gap-3 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3 text-xs text-[var(--color-fg-muted)]">
      <div>
        <p className="font-medium text-[var(--color-fg)]">Update ready</p>
        <p className="mt-1">{pendingUpdatesNote(pending)}</p>
      </div>
      <Button size="sm" variant="outline" onClick={() => (waiting > 0 ? setConfirming(true) : void apply())}>
        Apply now
      </Button>
      <ConfirmDialog
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={apply}
        title="Apply the update now?"
        description={runningTurnsWarning(waiting) ?? undefined}
        confirmLabel="Apply now"
        danger
      />
    </div>
  );
}
