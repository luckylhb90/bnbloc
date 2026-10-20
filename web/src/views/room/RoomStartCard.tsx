import { DoorOpenIcon } from "@phosphor-icons/react";
import type { RoomStart } from "@/lib/roomWelcome";
import { composerDrafts } from "@/lib/team";
import { Button } from "@/ui/Button";

// RoomStartCard is what an empty room shows every member: its welcome and
// quick prompts, the room's own (its settings edit them; a room component's
// install sets them). A chip puts its text into the message box; nothing is
// sent.
export function RoomStartCard({ roomId, start }: { readonly roomId: string; readonly start: RoomStart }) {
  return (
    <div role="status" data-room-start className="flex flex-col gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface-2)] px-4 py-3 text-sm">
      <div className="flex items-start gap-2">
        <DoorOpenIcon size={18} className="mt-0.5 shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <p className="min-w-0 flex-1 whitespace-pre-wrap text-[var(--color-fg-muted)]">{start.welcome || "Start from a quick prompt:"}</p>
      </div>
      {start.prompts.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5 pl-7">
          {start.prompts.map((q) => (
            <Button key={q.title + q.prompt} size="sm" variant="outline" onClick={() => composerDrafts.queue(roomId, q.prompt.trim())} title="Puts it into your message box — nothing is sent until you send it">
              {q.title || q.prompt}
            </Button>
          ))}
        </div>
      )}
    </div>
  );
}
