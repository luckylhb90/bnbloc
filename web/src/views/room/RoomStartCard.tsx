import { useState } from "react";
import { DoorOpenIcon, XIcon } from "@phosphor-icons/react";
import { clearRoomStart, takeRoomStart, type RoomInstallReply } from "@/lib/roomComponents";
import { ROOM_SOURCE_LABELS } from "@/lib/roomSources";
import { composerDrafts } from "@/lib/team";
import { Button } from "@/ui/Button";

// RoomStartCard follows up a room just made from a room component, on its
// first visits (the install reply, kept by CreateRoomFromComponentDialog):
// the component's welcome and quick prompts (a chip puts its text into the
// message box; nothing is sent), the scheduled tasks created paused, what
// was left out and why, and the sources the room asks to read, waiting for
// the organization admin's consent (the header's sources note asks them).
export function RoomStartCard({ roomId }: { readonly roomId: string }) {
  const [start, setStart] = useState<RoomInstallReply | undefined>(() => takeRoomStart(roomId));
  if (!start) return null;
  const dismiss = () => {
    clearRoomStart(roomId);
    setStart(undefined);
  };
  const paused = start.schedules.length;
  const prompts = (start.quickPrompts ?? []).filter((q) => q.prompt?.trim());
  return (
    <div role="status" data-room-start className="flex flex-col gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface-2)] px-4 py-3 text-sm">
      <div className="flex items-center gap-2">
        <DoorOpenIcon size={18} className="shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <span className="min-w-0 flex-1 font-medium">Made from {start.from} — finish setting up</span>
        <button
          type="button"
          onClick={dismiss}
          aria-label="Dismiss room setup"
          title="Dismiss"
          className="tactile grid size-7 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
        >
          <XIcon size={14} />
        </button>
      </div>
      {start.welcome && <p className="whitespace-pre-wrap text-[var(--color-fg-muted)]">{start.welcome}</p>}
      {prompts.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          {prompts.map((q) => (
            <Button key={q.title || q.prompt} size="sm" variant="outline" onClick={() => composerDrafts.queue(roomId, q.prompt.trim())} title="Puts it into your message box — nothing is sent until you send it">
              {q.title || q.prompt}
            </Button>
          ))}
        </div>
      )}
      {paused > 0 && (
        <p className="text-xs text-[var(--color-fg-muted)]">
          {paused} scheduled task{paused === 1 ? " was" : "s were"} created paused — open Schedules to turn {paused === 1 ? "it" : "them"} on.
        </p>
      )}
      {start.sources.map((s) => (
        <p key={s.name} className="text-xs text-[var(--color-fg-muted)]">
          Reading {ROOM_SOURCE_LABELS[s.name]} waits for your organization admin&apos;s consent — ask them from the note under the room&apos;s name.
        </p>
      ))}
      {start.skipped.length > 0 && (
        <div className="text-xs text-[var(--color-fg-muted)]">
          Left out:
          <ul className="list-disc pl-5">
            {start.skipped.map((s) => (
              <li key={s}>{s}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
