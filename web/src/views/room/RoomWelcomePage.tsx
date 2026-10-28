import { useMemo, useState } from "react";
import { HashIcon } from "@phosphor-icons/react";
import type { Channel, RoomDetail } from "@/lib/api";
import { channelsApi } from "@/lib/channelsApi";
import { errorText } from "@/lib/errors";
import { composerDrafts } from "@/lib/team";
import type { RoomSection } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { WhereToAsk } from "@/views/room/WhereToAsk";

// RoomWelcomePage is the room's welcome page (docs/prd/2026-10-28-channels-
// and-threads.md §2.5a): the welcome text, the room's rules, the
// recommended channels with what each is for, the open sections to join,
// the quick prompts, and "Where do I ask?". Shown on a member's first open
// of the room (this browser remembers it) and from the room header after.
// Joining a section here follows its channels (an open section's joining is
// following, C17); every join's outcome stays on its row.
export function RoomWelcomePage({
  room,
  channels,
  sections,
  onClose,
  onOpen,
  onChanged,
}: {
  readonly room: RoomDetail;
  readonly channels: readonly Channel[];
  readonly sections: readonly RoomSection[];
  readonly onClose: () => void;
  readonly onOpen: (channelId: string) => void;
  readonly onChanged: () => void;
}) {
  const [notes, setNotes] = useState<Record<string, string>>({});
  // The recommended channels by id, in the room's order; one archived or
  // deleted since is left out (the listing has only open channels).
  const recommended = useMemo(
    () => room.recommendedChannels.map((id) => channels.find((c) => c.channelId === id)).filter((c): c is Channel => !!c),
    [room.recommendedChannels, channels],
  );
  const join = async (key: string, ids: readonly string[]) => {
    try {
      for (const id of ids) await channelsApi.follow(roomId(room), id, { hidden: false });
      setNotes((n) => ({ ...n, [key]: "Joined: it is in your list" }));
      onChanged();
    } catch (e) {
      setNotes((n) => ({ ...n, [key]: errorText(e, "Could not join") }));
    }
  };
  const prompts = room.quickPrompts.filter((q) => q.prompt.trim());
  return (
    <Dialog open onClose={onClose} title={`Welcome to ${room.name || room.roomId}`} size="lg" footer={<Button variant="primary" onClick={onClose}>Start</Button>}>
      <div data-room-welcome className="flex flex-col gap-5 text-sm">
        {room.welcome.trim() && <p className="whitespace-pre-wrap">{room.welcome}</p>}
        {room.rules.trim() && (
          <section>
            <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Rules</h3>
            <p className="whitespace-pre-wrap text-[var(--color-fg-muted)]">{room.rules}</p>
          </section>
        )}
        {recommended.length > 0 && (
          <section>
            <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Recommended channels</h3>
            <ul className="flex flex-col gap-1.5">
              {recommended.map((c) => (
                <li key={c.channelId} className="flex items-start gap-2">
                  <HashIcon size={15} className="mt-0.5 shrink-0 text-[var(--color-fg-subtle)]" />
                  <div className="min-w-0 flex-1">
                    <button type="button" className="font-medium hover:underline" onClick={() => onOpen(c.channelId)}>
                      {c.name}
                    </button>
                    {c.purpose && <span className="text-[var(--color-fg-muted)]"> · {c.purpose}</span>}
                    {notes[c.channelId] && <p role="status" className="text-xs text-[var(--color-fg-muted)]">{notes[c.channelId]}</p>}
                  </div>
                  {!c.joined && (
                    <Button size="sm" variant="outline" onClick={() => void join(c.channelId, [c.channelId])}>
                      Join
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )}
        {sections.length > 0 && (
          <section>
            <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Sections</h3>
            <ul className="flex flex-col gap-1.5">
              {sections.map((s) => {
                // decision: pending owner. Joining an open section follows its
                // channels (C17: joining an open section is only following);
                // which of them the PRD leaves open, so the member sees them
                // listed and joins them all with one click.
                const inside = channels.filter((c) => c.sectionId === s.sectionId);
                const unjoined = inside.filter((c) => !c.joined || c.hidden).map((c) => c.channelId);
                return (
                  <li key={s.sectionId} className="flex items-start gap-2">
                    <div className="min-w-0 flex-1">
                      <span className="font-medium">{s.name}</span>
                      <span className="text-[var(--color-fg-muted)]">
                        {" "}
                        · {inside.length} {inside.length === 1 ? "channel" : "channels"}
                        {inside.length > 0 && `: ${inside.map((c) => `#${c.name}`).join(", ")}`}
                      </span>
                      {notes[s.sectionId] && <p role="status" className="text-xs text-[var(--color-fg-muted)]">{notes[s.sectionId]}</p>}
                    </div>
                    {unjoined.length > 0 && (
                      <Button size="sm" variant="outline" onClick={() => void join(s.sectionId, unjoined)}>
                        Join its channels
                      </Button>
                    )}
                  </li>
                );
              })}
            </ul>
          </section>
        )}
        {prompts.length > 0 && (
          <section>
            <h3 className="mb-1 text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Quick prompts</h3>
            <div className="flex flex-wrap gap-1.5">
              {prompts.map((q) => (
                <Button
                  key={q.title + q.prompt}
                  size="sm"
                  variant="outline"
                  title="Puts it into your message box — nothing is sent until you send it"
                  onClick={() => {
                    composerDrafts.queue(room.roomId, q.prompt.trim());
                    onClose();
                  }}
                >
                  {q.title || q.prompt}
                </Button>
              ))}
            </div>
          </section>
        )}
        <WhereToAsk roomId={room.roomId} channels={channels} onOpen={onOpen} />
      </div>
    </Dialog>
  );
}

const roomId = (room: RoomDetail) => room.roomId;

/** hasWelcome: the room has something for its welcome page (it is shown on
 *  first open only then; the header always offers it). */
export function hasWelcome(room: RoomDetail): boolean {
  return !!(room.welcome.trim() || room.rules.trim() || room.recommendedChannels.length > 0);
}
