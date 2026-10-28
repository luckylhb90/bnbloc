import { useCallback, useEffect, useMemo, useState } from "react";
import { HashIcon, MagnifyingGlassIcon } from "@phosphor-icons/react";
import type { Channel } from "@/lib/api";
import { channelsApi } from "@/lib/channelsApi";
import { directoryMatches, mayManageChannel, sectionOf } from "@/lib/channelRules";
import { errorText } from "@/lib/errors";
import type { RoomMembership } from "@/lib/roomRoles";
import type { ChannelDirectoryEntry, RoomSection } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { LoadFailed } from "@/ui/QueryState";
import { UserName } from "@/views/UserName";
import { WhereToAsk } from "@/views/room/WhereToAsk";

// ChannelDirectoryDialog is the room's channel directory (C18): every open
// channel with its purpose, owner and followers, searched by name and
// purpose, joined or left in one click (joining is following, not access),
// and "Where do I ask?" answered by the AI from the channels' purposes and
// rules. A join's outcome stays on its row.
export function ChannelDirectoryDialog({
  roomId,
  onClose,
  onOpen,
  onChanged,
}: {
  readonly roomId: string;
  readonly onClose: () => void;
  readonly onOpen: (channelId: string) => void;
  readonly onChanged: () => void;
}) {
  const [list, setList] = useState<ChannelDirectoryEntry[] | null>(null);
  const [sections, setSections] = useState<RoomSection[]>([]);
  const [failed, setFailed] = useState("");
  const [q, setQ] = useState("");
  const [busy, setBusy] = useState("");
  const [notes, setNotes] = useState<Record<string, string>>({});
  const load = useCallback(async () => {
    try {
      const d = await channelsApi.directory(roomId);
      setList(d.channels);
      setSections(d.sections);
      setFailed("");
    } catch (e) {
      setFailed(errorText(e, "Could not load the channels"));
    }
  }, [roomId]);
  useEffect(() => void load(), [load]);
  const shown = useMemo(() => directoryMatches(list ?? [], q), [list, q]);
  const toggle = async (c: ChannelDirectoryEntry) => {
    setBusy(c.channelId);
    try {
      if (c.joined && !c.hidden) await channelsApi.unfollow(roomId, c.channelId);
      else await channelsApi.follow(roomId, c.channelId, { hidden: false });
      setNotes({ [c.channelId]: c.joined && !c.hidden ? "Left" : "Joined: it is in your list" });
      await load();
      onChanged();
    } catch (e) {
      setNotes({ [c.channelId]: errorText(e, "Could not change it") });
    } finally {
      setBusy("");
    }
  };
  return (
    <Dialog open onClose={onClose} title="Browse channels" description="Every open channel of this room. Joining puts it in your list; you can read any of them." size="lg">
      <div className="flex flex-col gap-3">
        <WhereToAsk roomId={roomId} channels={list ?? []} onOpen={onOpen} />
        <label className="flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] px-2">
          <MagnifyingGlassIcon size={15} className="text-[var(--color-fg-subtle)]" />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search by name or purpose" aria-label="Search channels" className="h-9 min-w-0 flex-1 bg-transparent text-sm focus:outline-none" />
        </label>
        {failed ? (
          <LoadFailed message={failed} onRetry={() => void load()} />
        ) : list === null ? (
          <p className="text-sm text-[var(--color-fg-subtle)]">Loading…</p>
        ) : shown.length === 0 ? (
          <p className="text-sm text-[var(--color-fg-subtle)]">{q ? "No channel matches." : "No channels."}</p>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-border)]">
            {shown.map((c) => {
              const following = c.joined && !c.hidden;
              const sec = sectionOf(c, sections);
              return (
                <li key={c.channelId} data-directory-channel={c.name} className="flex items-start gap-3 py-2.5">
                  <HashIcon size={16} className="mt-0.5 shrink-0 text-[var(--color-fg-subtle)]" />
                  <div className="min-w-0 flex-1">
                    <button type="button" onClick={() => onOpen(c.channelId)} className="text-left text-sm font-medium hover:underline">
                      {c.name}
                    </button>
                    {sec && <span className="ml-2 text-xs text-[var(--color-fg-subtle)]">in {sec.name}</span>}
                    {c.purpose && <p className="text-sm text-[var(--color-fg-muted)]">{c.purpose}</p>}
                    <p className="text-xs text-[var(--color-fg-subtle)]">
                      {c.ownerId && (
                        <>
                          Owner <UserName id={c.ownerId} /> ·{" "}
                        </>
                      )}
                      {c.followers} {c.followers === 1 ? "member" : "members"} joined
                    </p>
                    {notes[c.channelId] && <p role="status" className="text-xs text-[var(--color-fg-muted)]">{notes[c.channelId]}</p>}
                  </div>
                  {!c.isDefault && (
                    <Button size="sm" variant={following ? "ghost" : "outline"} disabled={busy === c.channelId} onClick={() => void toggle(c)}>
                      {following ? "Leave" : "Join"}
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </Dialog>
  );
}

// ArchivedChannelsDialog lists the room's archived channels: read-only,
// their history kept; open one to read it, or restore it (its managers).
export function ArchivedChannelsDialog({
  roomId,
  subject,
  me,
  sections,
  onClose,
  onOpen,
  onChanged,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly me: RoomMembership | null;
  readonly sections: readonly RoomSection[];
  readonly onClose: () => void;
  readonly onOpen: (channelId: string) => void;
  readonly onChanged: () => void;
}) {
  const [list, setList] = useState<Channel[] | null>(null);
  const [failed, setFailed] = useState("");
  const [notes, setNotes] = useState<Record<string, string>>({});
  const load = useCallback(async () => {
    try {
      setList((await channelsApi.list(roomId, true)).channels);
      setFailed("");
    } catch (e) {
      setFailed(errorText(e, "Could not load the archived channels"));
    }
  }, [roomId]);
  useEffect(() => void load(), [load]);
  const restore = async (c: Channel) => {
    try {
      await channelsApi.archive(roomId, c.channelId, false);
      setNotes({ [c.channelId]: "Restored" });
      await load();
      onChanged();
    } catch (e) {
      setNotes({ [c.channelId]: errorText(e, "Could not restore it") });
    }
  };
  return (
    <Dialog open onClose={onClose} title="Archived channels" description="Read-only; their history stays. A channel's owners restore it.">
      {failed ? (
        <LoadFailed message={failed} onRetry={() => void load()} />
      ) : list === null ? (
        <p className="text-sm text-[var(--color-fg-subtle)]">Loading…</p>
      ) : list.length === 0 ? (
        <p className="text-sm text-[var(--color-fg-subtle)]">No archived channels.</p>
      ) : (
        <ul className="flex flex-col divide-y divide-[var(--color-border)]">
          {list.map((c) => (
            <li key={c.channelId} className="flex items-center gap-3 py-2">
              <HashIcon size={15} className="shrink-0 text-[var(--color-fg-subtle)]" />
              <div className="min-w-0 flex-1">
                <button type="button" onClick={() => onOpen(c.channelId)} className="text-left text-sm font-medium hover:underline">
                  {c.name}
                </button>
                {c.purpose && <p className="truncate text-xs text-[var(--color-fg-muted)]">{c.purpose}</p>}
                {notes[c.channelId] && <p role="status" className="text-xs text-[var(--color-fg-muted)]">{notes[c.channelId]}</p>}
              </div>
              {me && mayManageChannel(me, subject, c, sectionOf(c, sections)) && (
                <Button size="sm" variant="outline" onClick={() => void restore(c)}>
                  Restore
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}
