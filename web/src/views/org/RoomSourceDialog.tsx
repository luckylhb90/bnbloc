import { useEffect, useState } from "react";
import { WarningIcon } from "@phosphor-icons/react";
import { isAbort, runJSONAssist, useAssistTask } from "@/lib/assist";
import { errorText } from "@/lib/errors";
import { scheduleLine } from "@/lib/roomComponents";
import {
  EXPLAIN_SOURCE_USE,
  ROOM_SOURCE_LABELS,
  SCOPE_LABELS,
  driftView,
  explainReply,
  type RoomSourceDetail,
  type RoomSourceGrant,
  type RoomSourceScope,
  type SourceUseExplanation,
} from "@/lib/roomSources";
import { roomSourcesApi } from "@/lib/roomSourcesApi";
import { RoomSourceScopeValues } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { ConfirmDialog, Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";

const heading = "text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]";
const alertBox = "flex items-start gap-1.5 rounded-[var(--radius)] border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-2 py-1.5 text-xs";

// RoomSourceDialog is the organization admin's decision on one room's ask
// to read organization records (docs/prd/2026-10-20-rooms-as-components.md
// §2.3, §7), from the ask as the gateway holds it now
// (GET /v1/tenant/room-sources/{roomId}/{source}): the purpose, the
// person a grant is read by (the room's owner), the room's members (they
// read what it posts), its owner's scheduled tasks and those changed since
// a grant, and an AI explanation of what they would do with the records
// (the built-in explain-source-use assist over the gateway's input, run
// once the ask is read) that flags a task going beyond the purpose. A
// shared room cannot be granted. The admin picks the scope and grants (a
// grant again consents to the tasks as they are now), acknowledges the
// changes, or withdraws; AI proposes, the admin decides.
export function RoomSourceDialog({ grant, onClose, onDecided }: { readonly grant: RoomSourceGrant; readonly onClose: () => void; readonly onDecided: () => void }) {
  const toast = useToast();
  const explain = useAssistTask(EXPLAIN_SOURCE_USE)?.name ?? "";
  const [detail, setDetail] = useState<RoomSourceDetail | null>(null);
  const [scope, setScope] = useState<RoomSourceScope>(grant.scope ?? "opted-in");
  const [ai, setAi] = useState<SourceUseExplanation | null>(null);
  const [aiState, setAiState] = useState<"idle" | "running" | "failed">("idle");
  const [busy, setBusy] = useState(false);
  const [confirmWithdraw, setConfirmWithdraw] = useState(false);
  const [failure, setFailure] = useState("");

  useEffect(() => {
    let live = true;
    roomSourcesApi
      .detail(grant.roomId, grant.source)
      .then((d) => live && setDetail(d))
      .catch((e) => live && setFailure(errorText(e, "Could not read the room")));
    return () => {
      live = false;
    };
  }, [grant.roomId, grant.source]);

  const input = detail?.explainInput ?? "";
  useEffect(() => {
    if (!explain || !input) return;
    const ctl = new AbortController();
    setAiState("running");
    runJSONAssist(explain, { text: input }, ctl.signal)
      .then((r) => {
        setAi(explainReply(r.fields ?? {}));
        setAiState("idle");
      })
      .catch((e) => {
        if (!isAbort(e)) setAiState("failed");
      });
    return () => ctl.abort();
  }, [explain, input]);

  const current = detail?.grant ?? grant;
  const drift = detail?.drift ?? [];
  const mayGrant = !!detail && !detail.shared && !!detail.owner;

  const decide = async (withdraw: boolean) => {
    if (!withdraw && !detail) return;
    setBusy(true);
    setFailure("");
    try {
      if (withdraw) await roomSourcesApi.withdraw(grant.roomId, grant.source);
      else await roomSourcesApi.grant(grant.roomId, grant.source, scope, detail?.owner ?? "");
      toast.success(withdraw ? `Withdrawn: ${grant.roomName} reads nothing from its next run` : `Granted to ${grant.roomName}: ${SCOPE_LABELS[scope].label.toLowerCase()}`);
      onDecided();
      onClose();
    } catch (e) {
      setFailure(errorText(e, "Could not save the decision"));
    } finally {
      setBusy(false);
    }
  };

  const acknowledge = async () => {
    setBusy(true);
    setFailure("");
    try {
      setDetail(await roomSourcesApi.acknowledge(grant.roomId, grant.source));
      toast.success("Acknowledged: the tasks as they are now are what you consented to");
      onDecided();
    } catch (e) {
      setFailure(errorText(e, "Could not acknowledge the changes"));
    } finally {
      setBusy(false);
    }
  };

  let grantLabel = "Grant";
  if (busy) grantLabel = "Saving…";
  else if (current.scope) grantLabel = scope === current.scope ? "Grant again" : "Change grant";
  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title={`${grant.roomName} asks to read ${ROOM_SOURCE_LABELS[grant.source]}`}
      description="Its owner's scheduled tasks read the requests, replies, agents and outcomes of the conversations your grant covers, and its members read what they post. Members of the covered rooms are told which room reviews them; people's names reach it only when its owner is an organization admin."
      footer={
        <>
          {current.scope && (
            <Button variant="danger" onClick={() => setConfirmWithdraw(true)} disabled={busy}>
              Withdraw
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void decide(false)} disabled={busy || !mayGrant || (scope === current.scope && drift.length === 0)}>
            {grantLabel}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4 text-sm">
        <p className="text-[var(--color-fg-muted)]">
          {grant.from ? <>Installed from {grant.from} by <UserName id={grant.askedBy} />.</> : <>Asked in the room by <UserName id={grant.askedBy} />.</>}
        </p>
        {!detail ? (
          !failure && <p className="text-xs text-[var(--color-fg-subtle)]" role="status">Reading the room…</p>
        ) : (
          <ReadingRoom detail={detail} />
        )}
        <section className="flex flex-col gap-1">
          <span className={heading}>Stated purpose</span>
          <p className="whitespace-pre-wrap">{grant.purpose}</p>
        </section>
        {drift.length > 0 && (
          <section aria-label="Changed since granted" className="flex flex-col gap-2">
            <span className={heading}>Changed since you granted it</span>
            {drift.map(driftView).map((d) => (
              <div key={d.scheduleId} className="flex flex-col gap-1 rounded-[var(--radius)] border border-[var(--color-border)] px-2 py-1.5 text-xs">
                <span className="font-medium">{d.change === "added" ? "A task added" : "A task changed"}</span>
                {d.before && <del className="whitespace-pre-wrap text-[var(--color-fg-subtle)]">{d.before}</del>}
                <ins className="whitespace-pre-wrap no-underline">{d.after}</ins>
                <p className={d.beyond ? alertBox : "text-[var(--color-fg-muted)]"} role={d.beyond ? "alert" : undefined}>
                  {d.beyond && <WarningIcon size={14} className="mt-0.5 shrink-0 text-[var(--color-danger)]" aria-hidden="true" />}
                  {d.beyond ? `Beyond the stated purpose (AI): ${d.explanation}` : `AI: ${d.explanation}`}
                </p>
              </div>
            ))}
            <Button size="sm" variant="outline" className="self-start" onClick={() => void acknowledge()} disabled={busy}>
              Acknowledge the changes
            </Button>
          </section>
        )}
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

/** ReadingRoom is who a grant is read by and who reads what the room
 *  posts: its owner, its members with their roles, and its owner's
 *  scheduled tasks; a shared room is said to need to be private first. */
function ReadingRoom({ detail }: { readonly detail: RoomSourceDetail }) {
  return (
    <>
      {detail.shared && (
        <p role="alert" className={alertBox}>
          <WarningIcon size={14} className="mt-0.5 shrink-0 text-[var(--color-danger)]" aria-hidden="true" />
          This room is shared: anyone in the organization can join it and read what it reads. Its owner makes it private before you can grant it.
        </p>
      )}
      <section className="flex flex-col gap-1">
        <span className={heading}>Read by</span>
        <p>
          {detail.owner ? <UserName id={detail.owner} /> : "Nobody"}, the room&apos;s owner.{" "}
          <span className="text-xs text-[var(--color-fg-muted)]">Only their scheduled tasks read it; if the room changes owner, the grant waits for you again.</span>
        </p>
      </section>
      <section aria-label="Members" className="flex flex-col gap-1">
        <span className={heading}>Members ({detail.members.length}) read what it posts</span>
        <ul className="flex flex-wrap gap-1.5 text-xs">
          {detail.members.map((m) => (
            <li key={m.userId} className="rounded-full bg-[var(--color-surface-2)] px-2 py-0.5">
              <UserName id={m.userId} /> <span className="text-[var(--color-fg-subtle)]">· {m.role}</span>
            </li>
          ))}
        </ul>
        <p className="text-xs text-[var(--color-fg-subtle)]">You are told of each member who joins after a grant.</p>
      </section>
      <section className="flex flex-col gap-1">
        <span className={heading}>Its owner&apos;s scheduled tasks</span>
        {detail.tasks.length === 0 ? (
          <p className="text-xs text-[var(--color-fg-subtle)]">None yet: a task added later is told to you.</p>
        ) : (
          <ul className="list-disc pl-5 text-xs text-[var(--color-fg-muted)]">
            {detail.tasks.map((s) => (
              <li key={s.scheduleId} title={s.text}>
                {scheduleLine(s)}
              </li>
            ))}
          </ul>
        )}
      </section>
    </>
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
          <p role="alert" className={alertBox}>
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
