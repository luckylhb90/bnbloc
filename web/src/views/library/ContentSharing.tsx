import { useEffect, useState } from "react";
import { ArrowsClockwiseIcon, BuildingsIcon, ShareNetworkIcon } from "@phosphor-icons/react";
import { api, type RoomSummary } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import {
  canShare,
  hasPendingRequest,
  isPublishedVersion,
  orgPublishCopy,
  orgPublishToast,
  parseMembers,
  publicationStatus,
  shareSummary,
  updateAvailable,
  type ContentCopy,
  type ContentRoomShare,
} from "@/lib/contentSharing";
import { contentSharingApi } from "@/lib/contentSharingApi";
import { relativeTime } from "@/lib/format";
import { useTenantPolicy } from "@/lib/tenantPolicy";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";
import { errorText } from "@/lib/errors";

// Sharing and publishing in the Library (docs/prd/2026-10-12-experts-and-
// teams.md, step 4): an author shares a copy of a version with colleagues
// or a room, and asks the organization's admins to publish it; a recipient
// sees who shared a copy and updates it to the author's latest.

// ContentByline is a card's author line: you, the author of a copy, the
// author of an organization component, or the platform (the default
// content its operators publish).
export function ContentByline({ component: c }: { readonly component: ContentComponent }) {
  if (c.scope === "platform") return <>Default · for every organization</>;
  if (c.scope === "shared" && c.sharedBy) {
    return (
      <>
        Shared by <UserName id={c.sharedBy} />
      </>
    );
  }
  if (c.mine) return <>You</>;
  return (
    <>
      <UserName id={c.owner} />
      {c.scope === "org" ? " · Organization" : ""}
    </>
  );
}

// SharingStatus is the status line under a card: a copy's newer author
// version (with Update), or the author's organization publication.
export function SharingStatus({ component: c, onChanged }: { readonly component: ContentComponent; readonly onChanged: () => void }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  if (updateAvailable(c)) {
    const update = async () => {
      setBusy(true);
      try {
        await contentSharingApi.updateCopy(c.id);
        toast.success(`${contentLabel(c)} is now version ${c.sourceLatestVersion}`);
        onChanged();
      } catch (e) {
        toast.error(errorText(e, "Could not update it"));
      } finally {
        setBusy(false);
      }
    };
    return (
      <div className="flex items-center gap-2 text-xs text-[var(--color-fg-muted)]">
        <span className="min-w-0 flex-1">Version {c.sourceLatestVersion} is available from its author</span>
        <Button size="sm" variant="outline" onClick={() => void update()} disabled={busy}>
          <ArrowsClockwiseIcon size={13} />
          Update
        </Button>
      </div>
    );
  }
  const status = publicationStatus(c);
  if (!status) return null;
  return (
    <p className="text-xs">
      <Badge tone={status.tone === "muted" ? undefined : status.tone} title={status.text}>
        <BuildingsIcon size={12} />
        <span className="max-w-[16rem] truncate">{status.text}</span>
      </Badge>
    </p>
  );
}

// SharingActions are a card's Share and Publish buttons (the author's own
// components only).
export function SharingActions({
  component: c,
  onShare,
  onPublish,
}: {
  readonly component: ContentComponent;
  readonly onShare: (c: ContentComponent) => void;
  readonly onPublish: (c: ContentComponent) => void;
}) {
  if (!canShare(c)) return null;
  return (
    <>
      <button type="button" onClick={() => onShare(c)} title="Share a copy" aria-label={`Share ${c.name}`} className="tactile hover:text-[var(--color-fg)]">
        <ShareNetworkIcon size={15} />
      </button>
      <button type="button" onClick={() => onPublish(c)} title="Publish to organization" aria-label={`Publish ${c.name} to your organization`} className="tactile hover:text-[var(--color-fg)]">
        <BuildingsIcon size={15} />
      </button>
    </>
  );
}

type ShareTarget = "people" | "room";

// ShareDialog gives named colleagues, or everyone in a room, a copy of the
// latest version: theirs to use, unchanged by later edits until they
// update it.
export function ShareDialog({
  component: c,
  onClose,
}: {
  readonly component: ContentComponent | null;
  readonly onClose: () => void;
}) {
  const { subject } = useAuth();
  const toast = useToast();
  const [target, setTarget] = useState<ShareTarget>("people");
  const [members, setMembers] = useState("");
  const [rooms, setRooms] = useState<RoomSummary[] | null>(null);
  const [roomId, setRoomId] = useState("");
  const [note, setNote] = useState("");
  const [copies, setCopies] = useState<readonly ContentCopy[] | null>(null);
  const [roomShares, setRoomShares] = useState<readonly ContentRoomShare[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!c) return;
    let live = true;
    setTarget("people");
    setMembers("");
    setNote("");
    setCopies(null);
    setRoomShares([]);
    contentSharingApi
      .shares(c.id)
      .then((r) => {
        if (!live) return;
        setCopies(r.copies ?? []);
        setRoomShares(r.rooms ?? []);
      })
      .catch(() => live && setCopies([]));
    if (subject) {
      api
        .myRooms(subject)
        .then((r) => {
          if (!live) return;
          setRooms(r.rooms ?? []);
          setRoomId(r.rooms?.[0]?.roomId ?? "");
        })
        .catch(() => live && setRooms([]));
    }
    return () => {
      live = false;
    };
  }, [c, subject]);

  const names = parseMembers(members);
  const ready = target === "people" ? names.length > 0 : !!roomId;
  const share = async () => {
    if (!c || !ready || busy) return;
    setBusy(true);
    try {
      const reply = await contentSharingApi.share(c.id, {
        ...(target === "people" ? { members: names } : { roomId }),
        ...(note.trim() ? { note: note.trim() } : {}),
      });
      if (reply.shared.length === 0 && !reply.room) toast.error(shareSummary(reply));
      else toast.success(shareSummary(reply));
      onClose();
    } catch (e) {
      toast.error(errorText(e, "Could not share it"));
    } finally {
      setBusy(false);
    }
  };

  const roomName = (id: string) => rooms?.find((r) => r.roomId === id)?.name || id;
  const revoke = async (roomId: string) => {
    if (!c) return;
    try {
      await contentSharingApi.revokeRoomShare(c.id, roomId);
      setRoomShares((cur) => cur.filter((r) => r.roomId !== roomId));
      toast.success(`People who join ${roomName(roomId)} no longer get a copy. Copies already given stay.`);
    } catch (e) {
      toast.error(errorText(e, "Could not stop sharing with the room"));
    }
  };

  return (
    <Dialog
      open={!!c}
      onClose={onClose}
      title={c ? `Share ${contentLabel(c)}` : "Share"}
      description={
        c
          ? `Each person gets their own copy of version ${c.latestVersion} in their Library, shared by you. Your later versions do not change it until they update it.`
          : undefined
      }
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void share()} disabled={!ready || busy}>
            <ShareNetworkIcon size={15} />
            {busy ? "Sharing…" : "Share"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Segment
          options={[
            { value: "people" as const, label: "People" },
            { value: "room" as const, label: "Everyone in a room" },
          ]}
          value={target}
          onChange={setTarget}
        />
        {target === "people" ? (
          <Field label="Members" hint="Member ids of your organization, separated by commas.">
            <Input value={members} onChange={(e) => setMembers(e.target.value)} placeholder="jack, bob" autoFocus />
          </Field>
        ) : rooms === null ? (
          <p className="text-sm text-[var(--color-fg-subtle)]">Loading rooms…</p>
        ) : rooms.length === 0 ? (
          <p className="text-sm text-[var(--color-fg-muted)]">You are not in a room yet.</p>
        ) : (
          <Field label="Room" hint="Its members get a copy now, and people who join later get one too until you stop sharing with the room.">
            <Select value={roomId} onChange={(e) => setRoomId(e.target.value)}>
              {rooms.map((r) => (
                <option key={r.roomId} value={r.roomId}>
                  {r.name || r.roomId}
                </option>
              ))}
            </Select>
          </Field>
        )}
        <Field label="Note (optional)" hint="Sent with the notification.">
          <Textarea value={note} onChange={(e) => setNote(e.target.value)} rows={2} maxLength={500} />
        </Field>
        {roomShares.length > 0 && (
          <div className="flex flex-col gap-1">
            <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Shared with rooms</span>
            <ul className="flex flex-col gap-0.5 text-sm">
              {roomShares.map((rs) => (
                <li key={rs.roomId} className="flex items-center gap-2">
                  <span className="min-w-0 truncate">{roomName(rs.roomId)}</span>
                  <span className="text-xs text-[var(--color-fg-subtle)]">
                    version {rs.version} · new members get it
                  </span>
                  <Button size="sm" variant="ghost" className="ml-auto" onClick={() => void revoke(rs.roomId)}>
                    Stop
                  </Button>
                </li>
              ))}
            </ul>
          </div>
        )}
        {copies && copies.length > 0 && (
          <div className="flex flex-col gap-1">
            <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Shared with</span>
            <ul className="flex flex-col gap-0.5 text-sm">
              {copies.map((cp) => (
                <li key={cp.componentId} className="flex items-center gap-2">
                  <UserName id={cp.member} />
                  <span className="text-xs text-[var(--color-fg-subtle)]">
                    version {cp.version} · {relativeTime(cp.updatedAtMs)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </Dialog>
  );
}

// PublishDialog asks the organization's admins to publish the latest
// version, or takes the component back out of the organization.
export function PublishDialog({
  component: c,
  onClose,
  onChanged,
}: {
  readonly component: ContentComponent | null;
  readonly onClose: () => void;
  readonly onChanged: () => void;
}) {
  const toast = useToast();
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const approval = useTenantPolicy()?.publishApproval;
  useEffect(() => setNote(""), [c]);
  if (!c) return null;
  const copy = orgPublishCopy(approval, c.latestVersion);
  const status = publicationStatus(c);
  const published = isPublishedVersion(c);
  const canWithdraw = hasPendingRequest(c) || c.publication?.orgStatus === "active";
  const act = async (scope: "org" | "personal") => {
    setBusy(true);
    try {
      const reply = await contentSharingApi.publish(c.id, scope, scope === "org" ? note.trim() : undefined);
      toast.success(scope === "org" ? orgPublishToast(reply) : reply.note);
      onChanged();
      onClose();
    } catch (e) {
      toast.error(errorText(e, scope === "org" ? "Could not ask to publish it" : "Could not take it back"));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onClose={onClose}
      title={`Publish ${contentLabel(c)} to your organization`}
      description={copy.description}
      footer={
        <>
          {canWithdraw && (
            <Button variant="ghost" onClick={() => void act("personal")} disabled={busy}>
              Take it back
            </Button>
          )}
          <div className="flex-1" />
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void act("org")} disabled={busy || published}>
            <BuildingsIcon size={15} />
            {published ? "Already published" : busy ? "Sending…" : copy.action}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {status && <p className="text-sm text-[var(--color-fg-muted)]">{status.text}.</p>}
        {c.permissions.length > 0 && (
          <p className="text-sm text-[var(--color-fg-muted)]">
            It declares permissions ({c.permissions.join(", ")}): admins see them, and other members' runs of it are sandboxed.
          </p>
        )}
        <Field label={approval === "none" ? "Note (optional)" : "Note for the admins (optional)"} hint="What it is for, and why your organization needs it.">
          <Textarea value={note} onChange={(e) => setNote(e.target.value)} rows={3} maxLength={500} disabled={published} />
        </Field>
      </div>
    </Dialog>
  );
}
