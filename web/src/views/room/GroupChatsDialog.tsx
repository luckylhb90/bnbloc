import { useCallback, useEffect, useState } from "react";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { useCopy } from "@/ui/useCopy";
import { ChatsCircleIcon, CopyIcon, PlusIcon, TrashIcon } from "@phosphor-icons/react";
import { roomGroupsApi } from "@/lib/roomGroupsApi";
import type { RoomGroup, RoomGroupConnect } from "@/lib/wire.gen";
import { errorText } from "@/lib/errors";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";

// A connect code's life (wire.RoomGroupCodeTTLMs): a connection made after
// the code was issued is that code's.
const CODE_TTL_MS = 10 * 60 * 1000;

// GroupChatsDialog connects group chats to the room (docs/prd/2026-10-15-
// app-identity-schedules-notify-ai.md §5a): a member names the group and
// gets a one-time code to post in the chat where the organization's bot
// is; the room's apps and AI tasks then notify it as "group:<name>". The
// list refreshes while a code is open, so the connection shows when the
// bot binds it.
export function GroupChatsDialog({ roomId, mayChange, onClose }: { readonly roomId: string; readonly mayChange: boolean; readonly onClose: () => void }) {
  const toast = useToast();
  const copy = useCopy();
  const [groups, setGroups] = useState<RoomGroup[] | null>(null);
  const [name, setName] = useState("");
  const [code, setCode] = useState<RoomGroupConnect | null>(null);
  const [busy, setBusy] = useState(false);

  const reload = useCallback(() => {
    roomGroupsApi
      .list(roomId)
      .then((r) => setGroups(r.groups))
      .catch((e) => {
        setGroups([]);
        toast.error(errorText(e, "Could not load the group chats"));
      });
  }, [roomId, toast]);
  useEffect(() => reload(), [reload]);

  // While a code is open, look for its connection every few seconds.
  useVisiblePoll(
    () => {
      if (!code) return;
      if (Date.now() > code.expiresAtMs) {
        setCode(null);
        return;
      }
      roomGroupsApi
        .list(roomId)
        .then((r) => {
          setGroups(r.groups);
          if (r.groups.some((g) => g.name === code.name && g.createdAtMs >= code.expiresAtMs - CODE_TTL_MS)) {
            toast.success(`Connected "${code.name}"`);
            setCode(null);
          }
        })
        .catch(() => {});
    },
    3000,
    code !== null,
  );

  const connect = async () => {
    if (!name.trim()) return;
    setBusy(true);
    try {
      setCode(await roomGroupsApi.connect(roomId, name.trim()));
      setName("");
    } catch (e) {
      toast.error(errorText(e, "Could not make a connect code"));
    } finally {
      setBusy(false);
    }
  };
  const remove = async (g: RoomGroup) => {
    try {
      await roomGroupsApi.remove(roomId, g.name);
      reload();
    } catch (e) {
      toast.error(errorText(e, "Could not disconnect the group chat"));
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title="Group chats"
      description={'Group chats this room\'s apps and AI tasks can notify, as "group:<name>".'}
      footer={
        <Button variant="primary" onClick={onClose}>
          Done
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        {groups === null ? (
          <p className="text-sm text-[var(--color-fg-muted)]">Loading…</p>
        ) : groups.length === 0 ? (
          <p className="flex items-center gap-2 text-sm text-[var(--color-fg-muted)]">
            <ChatsCircleIcon size={18} /> No group chat connected yet.
          </p>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {groups.map((g) => (
              <li key={g.name} className="flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-2 text-sm">
                <span className="min-w-0 flex-1 truncate">
                  <span className="font-medium">{g.name}</span>
                  <span className="text-xs text-[var(--color-fg-muted)]">
                    {" "}
                    · group:{g.name} · by <UserName id={g.connectedBy} /> · {g.notifier}
                  </span>
                </span>
                {mayChange && (
                  <Button size="sm" variant="ghost" onClick={() => void remove(g)} aria-label={`Disconnect ${g.name}`} title="Disconnect">
                    <TrashIcon size={14} />
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
        {code ? (
          <div className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] p-3 text-sm">
            <p>
              Add the organization&apos;s bot to the group chat, then send this code there (within 10 minutes):
            </p>
            <div className="flex items-center gap-2">
              <code className="rounded bg-[var(--color-surface)] px-2 py-1 font-mono text-base tracking-wider">{code.code}</code>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => void copy(code.code, "code")}
                aria-label="Copy the code"
              >
                <CopyIcon size={14} /> Copy
              </Button>
            </div>
            <p className="text-xs text-[var(--color-fg-muted)]">Waiting for the bot to see it as &quot;{code.name}&quot;…</p>
          </div>
        ) : (
          mayChange && (
            <div className="flex items-end gap-2">
              <div className="min-w-0 flex-1">
                <Field label="Connect a group chat">
                  <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="weekly-report" maxLength={64} />
                </Field>
              </div>
              <Button variant="primary" onClick={() => void connect()} disabled={busy || !name.trim()}>
                <PlusIcon size={14} /> Get a code
              </Button>
            </div>
          )
        )}
      </div>
    </Dialog>
  );
}
