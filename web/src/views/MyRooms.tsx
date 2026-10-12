import { useEffect, useState } from "react";
import {
  ChatsCircleIcon,
  TicketIcon,
  RocketLaunchIcon,
  UsersIcon,
  GlobeIcon,
  LockIcon,
  DeviceMobileIcon,
} from "@phosphor-icons/react";
import { api, ApiError, type Room } from "@/lib/api";
import { isSubmitEnter } from "@/lib/keys";
import { useAuth } from "@/lib/auth";
import { Button } from "@/ui/Button";
import { Input, Field } from "@/ui/Input";
import { Card, Badge, Skeleton } from "@/ui/Card";
import { Dialog } from "@/ui/Dialog";
import { PageHeader, EmptyState } from "@/ui/Page";
import { useToast } from "@/ui/Toast";
import { RunAgentDialog } from "@/views/RunAgentDialog";
import { HandoffImportButton } from "@/views/HandoffImportButton";
import { useLocalAgent } from "@/lib/useLocalAgent";
import { WelcomeCard } from "@/views/WelcomeCard";

export function MyRooms({ onOpen }: { onOpen: (id: string) => void }) {
  const { subject } = useAuth();
  const [rooms, setRooms] = useState<Room[] | null>(null);
  const [loadError, setLoadError] = useState(false);
  const [unread, setUnread] = useState<Record<string, number>>({});
  const [redeeming, setRedeeming] = useState(false);
  const [running, setRunning] = useState(false);
  const [phone, setPhone] = useState(false);
  // Path A ("Run local agent") makes the gateway dial the caller's daemon, which
  // only works when a local core is reachable. Against a remote gateway it 403s,
  // so we hide it there and steer to the chat-drives-agent flow instead.
  const { isRemoteGateway } = useLocalAgent(true);
  const toast = useToast();

  const load = () => {
    if (!subject) return;
    setRooms(null);
    setLoadError(false);
    api
      .myRooms(subject)
      .then((r) => {
        const list = r.rooms || [];
        setRooms(list);
        // Fetch unread counts for the loaded rooms (best-effort; presentational).
        const ids = list.map((x) => x.roomId);
        if (ids.length > 0) {
          api
            .roomsUnread(ids)
            .then((u) => setUnread(u.unread || {}))
            .catch(() => setUnread({}));
        }
      })
      .catch(() => setLoadError(true));
  };

  useEffect(load, [subject]);

  return (
    <>
      <PageHeader
        title="My rooms"
        subtitle="Rooms you have joined"
        actions={
          <>
            <Button variant="ghost" size="icon" title="Use on phone" onClick={() => setPhone(true)}>
              <DeviceMobileIcon size={16} />
            </Button>
            <Button variant="outline" onClick={() => setRedeeming(true)}>
              <TicketIcon size={16} />
              Redeem invite
            </Button>
            <HandoffImportButton onImported={onOpen} />
            {!isRemoteGateway && (
              <Button variant="primary" onClick={() => setRunning(true)}>
                <RocketLaunchIcon size={16} />
                Run local agent
              </Button>
            )}
          </>
        }
      />

      <div className="p-6">
        <WelcomeCard rooms={rooms} onOpen={onOpen} />
        {loadError ? (
          <EmptyState
            icon={<ChatsCircleIcon size={24} />}
            title="Couldn't load your rooms"
            hint="Something went wrong reaching the server. Check your connection and try again."
            action={
              <Button variant="primary" onClick={load}>
                Retry
              </Button>
            }
          />
        ) : rooms === null ? (
          <div className="flex flex-col gap-3">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-20" />
            ))}
          </div>
        ) : rooms.length === 0 ? (
          <EmptyState
            icon={<ChatsCircleIcon size={24} />}
            title="No rooms yet"
            hint={
              isRemoteGateway
                ? "Open the Plaza to browse and join shared rooms, or redeem an invite. To run your own agent, install the bnbloc desktop app on your machine, point it at this gateway, then @-message your agent in a room."
                : "Open the Plaza to browse and join shared rooms, or redeem an invite. You can collaborate with people right away — a local agent core is only needed to run your own agent."
            }
            action={
              <div className="flex flex-wrap items-center justify-center gap-2">
                <Button variant="primary" onClick={() => setRedeeming(true)}>
                  <TicketIcon size={16} />
                  Redeem invite
                </Button>
                {!isRemoteGateway && (
                  <Button variant="outline" onClick={() => setRunning(true)}>
                    <RocketLaunchIcon size={16} />
                    Run local agent
                  </Button>
                )}
              </div>
            }
          />
        ) : (
          (() => {
            const owned = rooms.filter((r) => r.isOwner);
            const joined = rooms.filter((r) => !r.isOwner);
            return (
              <div className="flex flex-col gap-8">
                <RoomSection
                  title="Created by me"
                  rooms={owned}
                  empty="You haven't created any rooms."
                  onOpen={onOpen}
                  unread={unread}
                />
                <RoomSection
                  title="Joined"
                  rooms={joined}
                  empty="You haven't joined any rooms."
                  onOpen={onOpen}
                  unread={unread}
                />
              </div>
            );
          })()
        )}
      </div>

      <RedeemDialog
        open={redeeming}
        onClose={() => setRedeeming(false)}
        onRedeemed={(id) => {
          setRedeeming(false);
          toast.success("Invite redeemed");
          load();
          onOpen(id);
        }}
      />
      <RunAgentDialog
        open={running}
        onClose={() => setRunning(false)}
        onStarted={(id) => {
          setRunning(false);
          toast.success("Local agent session started");
          load();
          onOpen(id);
        }}
      />
      <PhoneDialog open={phone} onClose={() => setPhone(false)} />
    </>
  );
}

function PhoneDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const url = window.location.origin;
  const toast = useToast();
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Use on your phone"
      description="Open this URL on your phone's browser and sign in with the same account — your desktop agent answers, replies stream back here."
      size="sm"
      footer={
        <Button
          variant="primary"
          onClick={() => {
            navigator.clipboard?.writeText(url).catch(() => {});
            toast.success("URL copied");
          }}
        >
          Copy URL
        </Button>
      }
    >
      <code className="tabular block rounded-[var(--radius)] bg-[var(--color-surface-2)] px-3 py-2 text-sm break-all">
        {url}
      </code>
      <p className="mt-2 text-xs text-[var(--color-fg-subtle)]">
        The phone must reach this URL — use a tunnel or VPN if off-network.
      </p>
    </Dialog>
  );
}

function RoomRow({
  room,
  onOpen,
  unread,
}: {
  room: Room;
  onOpen: (id: string) => void;
  unread?: number;
}) {
  const shared = room.visibility === "shared";
  return (
    <Card
      interactive
      onClick={() => onOpen(room.roomId)}
      className="flex items-center gap-4 p-4 animate-in"
    >
      <div className="grid size-10 shrink-0 place-items-center rounded-[var(--radius)] bg-[var(--color-surface-2)] text-[var(--color-fg-muted)]">
        <ChatsCircleIcon size={20} />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate font-medium">{room.name || room.roomId}</span>
          {room.role && <Badge tone="neutral">{room.role}</Badge>}
        </div>
        <p className="truncate text-sm text-[var(--color-fg-muted)]">
          {room.description || room.roomId}
        </p>
      </div>
      <div className="flex items-center gap-3 text-xs text-[var(--color-fg-subtle)]">
        {unread && unread > 0 ? (
          <span
            className="grid min-w-5 place-items-center rounded-full bg-[var(--color-accent)] px-1.5 text-xs font-semibold text-[var(--color-accent-fg,white)]"
            title={`${unread > 99 ? "99+" : unread} unread`}
          >
            {unread > 99 ? "99+" : unread}
          </span>
        ) : null}
        <span className="inline-flex items-center gap-1">
          {shared ? <GlobeIcon size={13} /> : <LockIcon size={13} />}
        </span>
        <span className="inline-flex items-center gap-1 tabular">
          <UsersIcon size={13} />
          {room.memberCount ?? 0}
        </span>
      </div>
    </Card>
  );
}

function RoomSection({
  title,
  rooms,
  empty,
  onOpen,
  unread,
}: {
  title: string;
  rooms: Room[];
  empty: string;
  onOpen: (id: string) => void;
  unread: Record<string, number>;
}) {
  return (
    <section>
      <div className="mb-3 flex items-center gap-2">
        <h2 className="text-sm font-medium text-[var(--color-fg)]">{title}</h2>
        {rooms.length > 0 && (
          <span className="tabular rounded-full bg-[var(--color-surface-2)] px-1.5 text-xs text-[var(--color-fg-subtle)]">
            {rooms.length}
          </span>
        )}
      </div>
      {rooms.length === 0 ? (
        <p className="rounded-[var(--radius)] border border-dashed border-[var(--color-border)] px-4 py-6 text-center text-sm text-[var(--color-fg-subtle)]">
          {empty}
        </p>
      ) : (
        <div className="flex flex-col gap-3">
          {rooms.map((r) => (
            <RoomRow key={r.roomId} room={r} onOpen={onOpen} unread={unread[r.roomId]} />
          ))}
        </div>
      )}
    </section>
  );
}

function RedeemDialog({
  open,
  onClose,
  onRedeemed,
}: {
  open: boolean;
  onClose: () => void;
  onRedeemed: (roomId: string) => void;
}) {
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const { reauth } = useAuth();

  const submit = async () => {
    if (!code.trim()) return;
    setBusy(true);
    setErr("");
    try {
      const r = await api.redeemInvite(code.trim());
      await reauth().catch(() => {});
      setCode("");
      onRedeemed(r?.roomId || "");
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : "redeem failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Redeem invite"
      description="Enter an invite code to join a room."
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} disabled={busy || !code.trim()}>
            {busy ? "Joining…" : "Join room"}
          </Button>
        </>
      }
    >
      <Field label="Invite code" error={err}>
        <Input
          autoFocus
          placeholder="XXXX-XXXX"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          onKeyDown={(e) => isSubmitEnter(e) && submit()}
        />
      </Field>
    </Dialog>
  );
}
