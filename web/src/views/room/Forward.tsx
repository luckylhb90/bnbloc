import { useContext, useEffect, useState } from "react";
import { ShareFatIcon } from "@phosphor-icons/react";
import { api } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { channelsApi } from "@/lib/channelsApi";
import { cn } from "@/lib/cn";
import { errorText } from "@/lib/errors";
import { excerpt, sourceAuthor, sourceText } from "@/lib/forward";
import { routeHash } from "@/lib/hashRoute";
import { mayChat } from "@/lib/roomRoles";
import type { ForwardRef, RoomChannel, RoomSummary } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { ActorName } from "@/views/UserName";
import { MessageExtrasContext } from "@/views/room/messageExtras";
import type { FeedItem } from "@/views/room/types";

type Source = { readonly state: "loading" } | { readonly state: "gone" } | { readonly state: "ok"; readonly author: string; readonly text: string };

// ForwardCard is a forward's source under it, read from the source room as
// the reader: a reader who may not read the source's channel is told it is
// not available to them, never its text. It links to the message where it is.
export function ForwardCard({ item, className }: { readonly item: FeedItem; readonly className?: string }) {
  const ref = item.forwardOf;
  const [source, setSource] = useState<Source>({ state: "loading" });
  useEffect(() => {
    if (!ref) return;
    let cancelled = false;
    channelsApi.message(ref.roomId, ref.seq).then(
      (m) => !cancelled && setSource({ state: "ok", author: sourceAuthor(m), text: sourceText(m) }),
      () => !cancelled && setSource({ state: "gone" }),
    );
    return () => {
      cancelled = true;
    };
  }, [ref]);
  if (!ref) return null;
  const href = routeHash({ kind: "room", roomId: ref.roomId, seq: ref.seq, channelId: ref.channelId });
  return (
    <a
      data-forward-card={ref.seq}
      href={href}
      className={cn("tactile block max-w-full rounded-[var(--radius)] border-l-2 border-[var(--color-accent)]/50 bg-[var(--color-surface-2)] px-2.5 py-1.5 text-xs no-underline", className)}
    >
      <span className="mb-0.5 flex items-center gap-1 text-[var(--color-fg-subtle)]">
        <ShareFatIcon size={11} />
        Forwarded
        {source.state === "ok" && source.author ? (
          <>
            {" from "}
            <ActorName actor={source.author} className="font-medium" />
          </>
        ) : null}
      </span>
      <span data-forward-text className="block whitespace-pre-wrap text-[var(--color-fg-muted)] [overflow-wrap:anywhere]">
        {source.state === "loading" ? "…" : source.state === "gone" ? "A message you cannot open here" : excerpt(source.text) || "(no text)"}
      </span>
    </a>
  );
}

// ForwardButton opens the forward dialog for a stored message.
export function ForwardButton({ seq }: { readonly seq: number }) {
  const extras = useContext(MessageExtrasContext);
  if (!extras?.onForward) return null;
  return (
    <button
      type="button"
      data-forward
      onClick={() => extras.onForward?.(seq)}
      title="Forward"
      aria-label="Forward"
      className="tactile flex items-center rounded-sm text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
    >
      <ShareFatIcon size={12} />
    </button>
  );
}

// ForwardDialog forwards one message to a channel of a room the member may
// post in, with their own words above it. The server checks they may read
// the source and post where it goes.
export function ForwardDialog({ source, onClose }: { readonly source: ForwardRef | null; readonly onClose: () => void }) {
  const { subject } = useAuth();
  const toast = useToast();
  const [rooms, setRooms] = useState<readonly RoomSummary[]>([]);
  const [roomId, setRoomId] = useState("");
  const [channels, setChannels] = useState<readonly RoomChannel[]>([]);
  const [channelId, setChannelId] = useState("");
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);

  useEffect(() => {
    if (!source || !subject) return;
    setRoomId(source.roomId);
    setText("");
    let cancelled = false;
    api.myRooms(subject).then(
      (r) => !cancelled && setRooms((r.rooms ?? []).filter((x) => mayChat({ role: x.role }))),
      () => !cancelled && setRooms([]),
    );
    return () => {
      cancelled = true;
    };
  }, [source, subject]);

  useEffect(() => {
    if (!roomId) return;
    let cancelled = false;
    channelsApi.list(roomId).then(
      (l) => {
        if (cancelled) return;
        const open = l.channels.filter((c) => c.kind !== "people" && !c.archivedAtMs && c.kind !== "thread");
        setChannels(open);
        setChannelId((cur) => (open.some((c) => c.channelId === cur) ? cur : (open.find((c) => c.isDefault)?.channelId ?? open[0]?.channelId ?? "")));
      },
      () => !cancelled && setChannels([]),
    );
    return () => {
      cancelled = true;
    };
  }, [roomId]);

  const send = () => {
    if (!source || !roomId || !channelId) return;
    setSending(true);
    channelsApi
      .forward(roomId, channelId, source, text.trim())
      .then(() => {
        toast.success("Forwarded");
        onClose();
      })
      .catch((e) => toast.error(errorText(e, "Could not forward the message")))
      .finally(() => setSending(false));
  };

  return (
    <Dialog
      open={source !== null}
      onClose={onClose}
      title="Forward message"
      description="Only people who can read the message where it is will see its text."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button data-forward-send variant="primary" disabled={!channelId || sending} onClick={send}>
            Forward
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-[var(--color-fg-muted)]">Room</span>
          <Select data-forward-room value={roomId} onChange={(e) => setRoomId(e.target.value)}>
            {rooms.map((r) => (
              <option key={r.roomId} value={r.roomId}>
                {r.name || r.roomId}
              </option>
            ))}
          </Select>
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-xs text-[var(--color-fg-muted)]">Channel</span>
          <Select data-forward-channel value={channelId} onChange={(e) => setChannelId(e.target.value)}>
            {channels.map((c) => (
              <option key={c.channelId} value={c.channelId}>
                #{c.name}
              </option>
            ))}
          </Select>
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-xs text-[var(--color-fg-muted)]">Add a note (optional)</span>
          <textarea
            data-forward-note
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={3}
            className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] px-2 py-1.5"
          />
        </label>
      </div>
    </Dialog>
  );
}
