import { useState } from "react";
import { ArrowsClockwiseIcon, LinkIcon, XCircleIcon } from "@phosphor-icons/react";
import { gatewayBase } from "@/lib/config";
import { errorText } from "@/lib/errors";
import { hookInstructions, hookURL, type RoomSchedule } from "@/lib/schedules";
import { schedulesApi } from "@/lib/schedulesApi";
import { Button } from "@/ui/Button";
import { ConfirmDialog, Dialog } from "@/ui/Dialog";
import { Textarea } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { useCopy } from "@/ui/useCopy";

// ScheduleHookRow is a webhook task's URL in the Schedules list: its URL and
// the requests refused there; its creator makes it or rotates its secret
// (shown once, in a dialog), and its creator, the room's owner or an admin
// revokes it.
export function ScheduleHookRow({
  roomId,
  schedule,
  mine,
  moderator,
  onChanged,
}: {
  readonly roomId: string;
  readonly schedule: RoomSchedule;
  readonly mine: boolean;
  readonly moderator: boolean;
  readonly onChanged: () => void;
}) {
  const toast = useToast();
  const copy = useCopy();
  const [shown, setShown] = useState<string | null>(null);
  const [revoking, setRevoking] = useState(false);
  const [busy, setBusy] = useState(false);
  const hook = schedule.hook;
  const url = hook ? hookURL(gatewayBase() || window.location.origin, hook.hookId) : "";

  const rotate = async () => {
    setBusy(true);
    try {
      const made = await schedulesApi.rotateHook(roomId, schedule.scheduleId);
      setShown(hookInstructions(hookURL(gatewayBase() || window.location.origin, made.hook.hookId), made.secret));
      onChanged();
    } catch (e) {
      toast.error(errorText(e, "Could not make the webhook"));
    } finally {
      setBusy(false);
    }
  };
  const revoke = async () => {
    setBusy(true);
    try {
      await schedulesApi.revokeHook(roomId, schedule.scheduleId);
      toast.success("Webhook revoked: its URL answers nothing now");
      onChanged();
    } catch (e) {
      toast.error(errorText(e, "Could not revoke the webhook"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="mt-2 flex flex-wrap items-center gap-1 rounded-[var(--radius)] bg-[var(--color-surface-2)] px-2 py-1.5 text-xs">
      <LinkIcon size={13} className="text-[var(--color-fg-subtle)]" />
      {hook ? (
        <>
          <code className="min-w-0 flex-1 truncate" title={url}>
            {url}
          </code>
          {(hook.refused ?? 0) > 0 && <span className="text-[var(--color-warning)]">{hook.refused} refused</span>}
        </>
      ) : (
        <span className="flex-1 text-[var(--color-fg-muted)]">{mine ? "No webhook URL yet" : "Its creator has not made its webhook URL"}</span>
      )}
      {(schedule.droppedEvents ?? 0) > 0 && <span className="text-[var(--color-warning)]">{schedule.droppedEvents} dropped over the limit</span>}
      {mine && (
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => void rotate()}>
          <ArrowsClockwiseIcon size={13} /> {hook ? "New secret" : "Make URL"}
        </Button>
      )}
      {hook && (mine || moderator) && (
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => setRevoking(true)}>
          <XCircleIcon size={13} /> Revoke
        </Button>
      )}
      {shown !== null && (
        <Dialog
          open
          onClose={() => setShown(null)}
          title="Webhook URL and secret"
          description="Give these to the system that sends the events. The secret is shown this once: make a new one if it is lost."
          footer={
            <>
              <Button
                variant="ghost"
                onClick={() => void copy(shown, "webhook URL and secret")}
              >
                Copy
              </Button>
              <Button variant="primary" onClick={() => setShown(null)}>
                Done
              </Button>
            </>
          }
        >
          <Textarea readOnly rows={6} value={shown} className="font-mono text-xs" />
        </Dialog>
      )}
      <ConfirmDialog
        open={revoking}
        onClose={() => setRevoking(false)}
        onConfirm={() => {
          setRevoking(false);
          void revoke();
        }}
        title="Revoke this webhook?"
        description="Its URL stops working at once; the task stays. Its creator can make a new URL."
        confirmLabel="Revoke"
        danger
      />
    </div>
  );
}
