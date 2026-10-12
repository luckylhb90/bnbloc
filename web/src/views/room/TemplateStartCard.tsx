import { useState } from "react";
import { SquaresFourIcon, XIcon } from "@phosphor-icons/react";
import { api } from "@/lib/api";
import { agentFollowUps, clearTemplateStart, takeTemplateStart, type RoomTemplateInstance } from "@/lib/roomTemplates";
import { agentIdentity } from "@/lib/roomAgents";
import { composerDrafts } from "@/lib/team";
import { Button } from "@/ui/Button";
import { useToast } from "@/ui/Toast";
import { displayNameOf } from "@/lib/useDisplayName";
import { UserName, UserNames } from "@/views/UserName";
import { errorText } from "@/lib/errors";

// TemplateStartCard follows up a room just created from a template: starter
// prompts drop into the composer, suggested agents' owners can be invited
// (owner/admin, the normal add-member path), and agents the member does not
// own need their owner's authorization — the card only helps ask for it.
// Nothing here grants or requests anything on its own.
export function TemplateStartCard({
  roomId,
  subject,
  members,
  canInvite,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly members: readonly string[];
  readonly canInvite: boolean;
}) {
  const toast = useToast();
  const [inst, setInst] = useState<RoomTemplateInstance | undefined>(() => takeTemplateStart(roomId));
  const [added, setAdded] = useState<string[]>([]);
  if (!inst || !subject) return null;

  const { invite, requestBorrow } = agentFollowUps(inst.suggestedAgents ?? [], subject, [...members, ...added]);
  const prompts = inst.starterPrompts ?? [];
  const failed = (inst.failedMembers ?? []).filter((m) => !members.includes(m) && !added.includes(m));
  const pausedSchedules = inst.schedules?.length ?? 0;
  if (!prompts.length && !invite.length && !requestBorrow.length && !failed.length && !pausedSchedules) return null;

  const dismiss = () => {
    clearTemplateStart(roomId);
    setInst(undefined);
  };
  const add = async (userId: string) => {
    try {
      await api.addMember(roomId, userId, "member");
      setAdded((a) => [...a, userId]);
      toast.success(`Added ${displayNameOf(userId)}`);
    } catch (e) {
      toast.error(errorText(e, "Add member failed"));
    }
  };
  const askOwner = (ownerId: string, provider: string) =>
    composerDrafts.queue(roomId, `@${ownerId} could you authorize me to use your ${provider} agent in this room?`);

  return (
    <div
      role="status"
      data-template-start
      className="flex flex-col gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface-2)] px-4 py-3 text-sm"
    >
      <div className="flex items-center gap-2">
        <SquaresFourIcon size={18} className="shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <span className="min-w-0 flex-1 font-medium">Created from a template — finish setting up</span>
        <button
          type="button"
          onClick={dismiss}
          aria-label="Dismiss template setup"
          title="Dismiss"
          className="tactile grid size-7 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
        >
          <XIcon size={14} />
        </button>
      </div>
      {prompts.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-[var(--color-fg-subtle)]">Starter prompts:</span>
          {prompts.map((p, i) => (
            <button
              key={i}
              type="button"
              onClick={() => composerDrafts.queue(roomId, p)}
              title={p}
              className="tactile max-w-[18rem] truncate rounded-full border border-[var(--color-border)] px-2.5 py-0.5 text-xs text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]"
            >
              {p}
            </button>
          ))}
        </div>
      )}
      {invite.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-[var(--color-fg-subtle)]">Suggested agent owners not in the room:</span>
          {invite.map((owner) =>
            canInvite ? (
              <Button key={owner} size="sm" variant="outline" onClick={() => void add(owner)}>
                Invite <UserName id={owner} />
              </Button>
            ) : (
              <UserName key={owner} id={owner} className="text-xs" />
            ),
          )}
        </div>
      )}
      {requestBorrow.length > 0 && (
        <div className="flex flex-col gap-1">
          <span className="text-xs text-[var(--color-fg-subtle)]">
            Using another member's agent needs their authorization in this room (they grant it from the handshake icon):
          </span>
          <div className="flex flex-wrap items-center gap-1.5">
            {requestBorrow.map((a) => (
              <Button key={agentIdentity(a)} size="sm" variant="ghost" onClick={() => askOwner(a.ownerId, a.provider)} title="Puts a request into your message box — nothing is sent until you send it">
                Ask <UserName id={a.ownerId} /> about their {a.provider}
              </Button>
            ))}
          </div>
        </div>
      )}
      {pausedSchedules > 0 && (
        <p className="text-xs text-[var(--color-fg-muted)]">
          {pausedSchedules} schedule{pausedSchedules === 1 ? " was" : "s were"} created paused — open Schedules to enable{" "}
          {pausedSchedules === 1 ? "it" : "them"}.
        </p>
      )}
      {failed.length > 0 && (
        <p className="text-xs text-[var(--color-danger)]">
          Could not add <UserNames ids={failed} /> — invite them from Members.
        </p>
      )}
    </div>
  );
}
