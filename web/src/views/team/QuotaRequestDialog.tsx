import { useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { useDisplayName } from "@/lib/useDisplayName";
import { formatAgentHandle } from "@/lib/displayNames";
import { errorText } from "@/lib/errors";
import { refreshQuotaRequests } from "@/lib/queries";

const MAX_NOTE = 500;

// QuotaRequestDialog lets a borrower whose borrow hit the owner's run limit ask
// the owner for more runs. The requester is the signed-in member (the server
// never takes it from the body); the owner approves or declines in-app.
export function QuotaRequestDialog({
  roomId,
  owner,
  provider,
  providerLabel,
  runQuota,
  onClose,
}: {
  readonly roomId: string;
  readonly owner: string;
  // The grant discriminator (borrowKey: "<core>:<provider>" or the provider).
  readonly provider: string;
  readonly providerLabel: string;
  readonly runQuota?: number;
  readonly onClose: () => void;
}) {
  const toast = useToast();
  const ownerName = useDisplayName(owner);
  const [runs, setRuns] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const runsValue = runs.trim() ? Number(runs) : undefined;
  const runsInvalid = runsValue !== undefined && (!Number.isInteger(runsValue) || runsValue < 1 || runsValue > 10000);

  const submit = async () => {
    if (runsInvalid) return;
    setBusy(true);
    try {
      await api.createQuotaRequest({ roomId, owner, provider, requestedRuns: runsValue, note: note.trim() });
      refreshQuotaRequests();
      toast.success(`Asked ${ownerName} for more runs`);
      onClose();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.info(e.message || "You already have a pending request for this agent");
        onClose();
        return;
      }
      toast.error(errorText(e, "Could not send the request"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      size="sm"
      title={`Request more runs of ${formatAgentHandle(ownerName, providerLabel)}`}
      description={
        runQuota !== undefined
          ? `You've used all ${runQuota} run${runQuota === 1 ? "" : "s"} ${ownerName} authorized in this room. ${ownerName} will see your request and can approve or decline it.`
          : `${ownerName} will see your request and can approve or decline it.`
      }
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={busy || runsInvalid} onClick={() => void submit()}>
            {busy ? "Sending…" : "Send request"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Runs" hint="Optional — leave empty to let the owner decide" error={runsInvalid ? "Enter a whole number from 1 to 10000" : undefined}>
          <Input
            type="number"
            min={1}
            max={10000}
            inputMode="numeric"
            value={runs}
            placeholder="e.g. 10"
            onChange={(e) => setRuns(e.target.value)}
          />
        </Field>
        <Field label="Note" hint={`${note.length}/${MAX_NOTE}`}>
          <Textarea
            rows={3}
            maxLength={MAX_NOTE}
            value={note}
            placeholder="What do you need the runs for?"
            onChange={(e) => setNote(e.target.value)}
          />
        </Field>
      </div>
    </Dialog>
  );
}
