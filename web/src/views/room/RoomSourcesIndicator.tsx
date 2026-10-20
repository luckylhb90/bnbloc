import { useId, useRef, useState } from "react";
import { EyeIcon, HourglassIcon } from "@phosphor-icons/react";
import { errorText } from "@/lib/errors";
import { routeHash } from "@/lib/hashRoute";
import { canInvite, type RoomMembership } from "@/lib/roomRoles";
import { ROOM_SOURCE_LABELS, reviewLine, sourceState, type RoomSourceAskState } from "@/lib/roomSources";
import { refreshRoomSources, roomSourcesApi, useRoomSources } from "@/lib/roomSourcesApi";
import { Button } from "@/ui/Button";
import { useToast } from "@/ui/Toast";
import { useDismissOnOutside } from "@/ui/useDismissOnOutside";
import { UserName } from "@/views/UserName";

// RoomSourcesIndicator is the room header's note on organization records
// (docs/prd/2026-10-20-rooms-as-components.md §2.3), beside the platform
// agents' one: to every member of a room a grant covers, which room
// reviews its conversation outcomes (linked when they are a member of
// it); in a room that asks to read a source, its purpose and where the
// ask stands, with "Ask admin" for its owner or admins while it waits.
export function RoomSourcesIndicator({ roomId, me }: { readonly roomId: string; readonly me: RoomMembership | null }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const panelId = useId();
  const { data } = useRoomSources(me ? roomId : "");
  useDismissOnOutside(open, ref, () => setOpen(false), { escape: true });

  const reviewed = data?.reviewedBy ?? [];
  const asks = data?.asks ?? [];
  if (reviewed.length === 0 && asks.length === 0) return null;
  const waiting = asks.some((a) => !a.scope);
  const label = reviewed.length > 0 ? "Outcomes reviewed" : waiting ? "Waiting for your organization admin" : "Reads organization records";
  const Icon = reviewed.length === 0 && waiting ? HourglassIcon : EyeIcon;
  return (
    <div ref={ref} className="relative min-w-0">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        title={reviewed.length > 0 ? reviewLine(reviewed[0]) : label}
        style={{ minHeight: 0, minWidth: 0 }}
        className="tap tactile flex max-w-full shrink-0 items-center gap-1 rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface-2)] px-2 py-0.5 text-[11px] font-medium text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
      >
        <Icon size={12} weight="fill" className="shrink-0" aria-hidden="true" />
        <span className="truncate">{label}</span>
      </button>
      {open && (
        <div
          id={panelId}
          role="dialog"
          aria-label="Organization records"
          className="animate-in z-30 mt-2 max-sm:fixed max-sm:inset-x-4 sm:absolute sm:left-0 sm:top-full sm:w-80 rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-elevated)] p-3 text-sm shadow-[var(--shadow-pop)]"
        >
          {reviewed.map((r) => (
            <div key={`${r.source}:${r.roomId ?? r.roomName}`} className="mb-2">
              <p className="font-medium text-[var(--color-fg)]">{reviewLine(r)}</p>
              <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
                The scheduled tasks of <UserName id={r.grantee} />, its owner, read the requests, replies and outcomes of this room&apos;s agent
                conversations, as your organization&apos;s admin granted. {r.roomId ? <a className="underline" href={routeHash({ kind: "room", roomId: r.roomId })}>Open it</a> : "You are not a member of it."}
              </p>
            </div>
          ))}
          {asks.map((a) => (
            <AskLine key={a.source} roomId={roomId} ask={a} mayAsk={!!me && canInvite(me)} />
          ))}
        </div>
      )}
    </div>
  );
}

/** AskLine is one source the room asks to read: its purpose, where it
 *  stands and, while it waits, "Ask admin" for the room's editors. */
function AskLine({ roomId, ask, mayAsk }: { readonly roomId: string; readonly ask: RoomSourceAskState; readonly mayAsk: boolean }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const askAdmin = async () => {
    setBusy(true);
    try {
      const r = await roomSourcesApi.askAdmin(roomId, ask.source);
      if (r.queued) toast.success("Your organization's admins were asked");
      else toast.info("No admin could be notified now (or they were asked today): tell them to open Organization → Room sources");
      refreshRoomSources();
    } catch (e) {
      toast.error(errorText(e, "Could not ask the admins"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex flex-col gap-1">
      <p className="font-medium text-[var(--color-fg)]">This room asks to read {ROOM_SOURCE_LABELS[ask.source]}</p>
      <p className="text-xs text-[var(--color-fg-muted)]">To: {ask.purpose}</p>
      <p className="text-xs text-[var(--color-fg-subtle)]">
        {sourceState(ask.scope)}
        {ask.scope && ask.grantee && (
          <>
            , read by <UserName id={ask.grantee} />: only their scheduled tasks read it, and the room&apos;s members read what they post
          </>
        )}
      </p>
      {!ask.scope && mayAsk && (
        <Button size="sm" variant="outline" onClick={() => void askAdmin()} disabled={busy} className="self-start">
          {busy ? "Asking…" : "Ask admin"}
        </Button>
      )}
    </div>
  );
}
