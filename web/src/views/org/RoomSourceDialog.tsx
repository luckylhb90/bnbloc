import { useEffect, useState } from "react";
import { WarningIcon } from "@phosphor-icons/react";
import { isAbort, runJSONAssist, useAssistTask } from "@/lib/assist";
import { errorText } from "@/lib/errors";
import { scheduleLine } from "@/lib/roomComponents";
import { EXPLAIN_SOURCE_USE, ROOM_SOURCE_LABELS, SCOPE_LABELS, explainReply, explainSourceInput, type RoomSourceGrant, type RoomSourceScope, type SourceUseExplanation } from "@/lib/roomSources";
import { roomSourcesApi } from "@/lib/roomSourcesApi";
import { RoomSourceScopeValues } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { ConfirmDialog, Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";

const heading = "text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]";

// RoomSourceDialog is the organization admin's decision on one room's ask
// to read organization records (docs/prd/2026-10-20-rooms-as-components.md
// §2.3): the purpose the room's package states, its scheduled tasks, and an
// AI explanation of what they would do with the records (the built-in
// explain-source-use assist, run once when the dialog opens) that flags a
// task going beyond the purpose. The admin picks the scope and grants, or
// withdraws a grant; AI proposes, the admin decides.
export function RoomSourceDialog({ grant, onClose, onDecided }: { readonly grant: RoomSourceGrant; readonly onClose: () => void; readonly onDecided: () => void }) {
  const toast = useToast();
  const explain = useAssistTask(EXPLAIN_SOURCE_USE)?.name ?? "";
  const [scope, setScope] = useState<RoomSourceScope>(grant.scope ?? "opted-in");
  const [ai, setAi] = useState<SourceUseExplanation | null>(null);
  const [aiState, setAiState] = useState<"idle" | "running" | "failed">("idle");
  const [busy, setBusy] = useState(false);
  const [confirmWithdraw, setConfirmWithdraw] = useState(false);
  const [failure, setFailure] = useState("");

  useEffect(() => {
    if (!explain) return;
    const ctl = new AbortController();
    setAiState("running");
    runJSONAssist(explain, { text: explainSourceInput(grant) }, ctl.signal)
      .then((r) => {
        setAi(explainReply(r.fields ?? {}));
        setAiState("idle");
      })
      .catch((e) => {
        if (!isAbort(e)) setAiState("failed");
      });
    return () => ctl.abort();
  }, [explain, grant]);

  const decide = async (withdraw: boolean) => {
    setBusy(true);
    setFailure("");
    try {
      if (withdraw) await roomSourcesApi.withdraw(grant.roomId, grant.source);
      else await roomSourcesApi.grant(grant.roomId, grant.source, scope);
      toast.success(withdraw ? `Withdrawn: ${grant.roomName} reads nothing from its next run` : `Granted to ${grant.roomName}: ${SCOPE_LABELS[scope].label.toLowerCase()}`);
      onDecided();
      onClose();
    } catch (e) {
      setFailure(errorText(e, "Could not save the decision"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title={`${grant.roomName} asks to read ${ROOM_SOURCE_LABELS[grant.source]}`}
      description="Its scheduled tasks read the requests, replies, agents and outcomes of the conversations your grant covers. Members of those rooms are told which room reviews them; people's names reach it only when its task's owner is an organization admin."
      footer={
        <>
          {grant.scope && (
            <Button variant="danger" onClick={() => setConfirmWithdraw(true)} disabled={busy}>
              Withdraw
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void decide(false)} disabled={busy || scope === grant.scope}>
            {busy ? "Saving…" : grant.scope ? "Change grant" : "Grant"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4 text-sm">
        <p className="text-[var(--color-fg-muted)]">
          Installed from {grant.from} by <UserName id={grant.askedBy} />.
        </p>
        <section className="flex flex-col gap-1">
          <span className={heading}>Stated purpose</span>
          <p className="whitespace-pre-wrap">{grant.purpose}</p>
        </section>
        <section className="flex flex-col gap-1">
          <span className={heading}>Scheduled tasks of the component</span>
          {grant.schedules.length === 0 ? (
            <p className="text-xs text-[var(--color-fg-subtle)]">None (or the component&apos;s version is gone).</p>
          ) : (
            <ul className="list-disc pl-5 text-xs text-[var(--color-fg-muted)]">
              {grant.schedules.map((s, i) => (
                <li key={i} title={s.text}>
                  {scheduleLine(s)}
                </li>
              ))}
            </ul>
          )}
        </section>
        <AIExplanation available={!!explain} state={aiState} explanation={ai} />
        <fieldset className="flex flex-col gap-1">
          <legend className={heading}>Which rooms it may read</legend>
          {RoomSourceScopeValues.map((v) => (
            <label key={v} className="flex items-start gap-2 rounded-[var(--radius)] px-2 py-1 hover:bg-[var(--color-surface-2)]">
              <input type="radio" name="scope" className="mt-1" checked={scope === v} onChange={() => setScope(v)} />
              <span>
                <span className="font-medium">{SCOPE_LABELS[v].label}</span>
                <span className="block text-xs text-[var(--color-fg-muted)]">{SCOPE_LABELS[v].hint}</span>
              </span>
            </label>
          ))}
        </fieldset>
        {failure && <ErrorBox>{failure}</ErrorBox>}
      </div>
      <ConfirmDialog
        open={confirmWithdraw}
        onClose={() => setConfirmWithdraw(false)}
        onConfirm={() => decide(true)}
        title={`Withdraw ${grant.roomName}'s grant?`}
        description="Its scheduled tasks read nothing from their next read on. The room keeps what it already wrote."
        confirmLabel="Withdraw"
        danger
      />
    </Dialog>
  );
}

/** AIExplanation shows what the tasks would do with the records and flags
 *  what goes beyond the stated purpose. */
function AIExplanation({ available, state, explanation }: { readonly available: boolean; readonly state: "idle" | "running" | "failed"; readonly explanation: SourceUseExplanation | null }) {
  let body;
  if (!available) body = <p className="text-xs text-[var(--color-fg-subtle)]">No AI explanation: AI assists are off for your organization.</p>;
  else if (state === "running") body = <p className="text-xs text-[var(--color-fg-subtle)]" role="status">Reading the tasks…</p>;
  else if (state === "failed" || !explanation) body = <p className="text-xs text-[var(--color-fg-subtle)]">The AI explanation could not be made; read the tasks above.</p>;
  else
    body = (
      <>
        <p>{explanation.summary}</p>
        {explanation.uses.length > 0 && (
          <ul className="list-disc pl-5 text-xs text-[var(--color-fg-muted)]">
            {explanation.uses.map((u) => (
              <li key={u}>{u}</li>
            ))}
          </ul>
        )}
        {explanation.mismatch ? (
          <p role="alert" className="flex items-start gap-1.5 rounded-[var(--radius)] border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-2 py-1.5 text-xs">
            <WarningIcon size={14} className="mt-0.5 shrink-0 text-[var(--color-danger)]" aria-hidden="true" />
            Beyond the stated purpose: {explanation.mismatch}
          </p>
        ) : (
          <p className="text-xs text-[var(--color-fg-subtle)]">The tasks fit the stated purpose.</p>
        )}
      </>
    );
  return (
    <section aria-label="AI explanation" className="flex flex-col gap-1 rounded-[var(--radius-lg)] bg-[var(--color-surface-2)] px-3 py-2">
      <span className={heading}>What the tasks would do (AI)</span>
      {body}
    </section>
  );
}
