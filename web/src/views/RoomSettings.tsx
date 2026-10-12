import { useState } from "react";
import { CopyIcon, TrashIcon } from "@phosphor-icons/react";
import { api, ApiError, type Room } from "@/lib/api";
import { Button } from "@/ui/Button";
import { Input, Field } from "@/ui/Input";
import { Segment, type SegmentOption } from "@/ui/Segment";
import { Dialog, ConfirmDialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";
import { useCopy } from "@/ui/useCopy";
import { displayNameOf } from "@/lib/useDisplayName";
import { EmailInput } from "@/views/UserName";
import { RoomContentSection } from "@/views/room/RoomContentSection";

const syncOptions: SegmentOption<"git" | "mutagen" | "none">[] = [
  { value: "git", label: "Git" },
  { value: "mutagen", label: "Live sync" },
  { value: "none", label: "None" },
];

const syncGuidance: Record<string, string> = {
  git: "Members share one Git remote and branch; each agent works in its own clone. The room coordinates; Git owns the code.",
  mutagen:
    "Each member runs a bridge that mirrors the project directory in real time over the relay. Requires per-member bridges — verify before relying on it.",
  none: "Chat and agent activity only; code is not synced.",
};

export function RoomSettings({
  room,
  onClose,
  onChanged,
  onDeleted,
}: {
  room: Room;
  onClose: () => void;
  onChanged: (r: Room) => void;
  onDeleted: () => void;
}) {
  const [name, setName] = useState(room.name || "");
  const [desc, setDesc] = useState(room.description || "");
  const [vis, setVis] = useState<"private" | "shared">(
    room.visibility === "shared" ? "shared" : "private",
  );
  const [sync, setSync] = useState<"git" | "mutagen" | "none">(
    (room.syncMode as "git" | "mutagen" | "none") || "git",
  );
  const [invite, setInvite] = useState("");
  const [inviteRole, setInviteRole] = useState<"admin" | "member" | "visitor">("member");
  const [inviteTtl, setInviteTtl] = useState<"1d" | "7d" | "30d">("7d");
  const [inviteCap, setInviteCap] = useState<"1" | "5" | "25" | "0">("1");
  const [newMember, setNewMember] = useState("");
  const [newMemberRole, setNewMemberRole] = useState<"admin" | "member" | "visitor">("member");
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [transferTo, setTransferTo] = useState("");
  const [confirmTransfer, setConfirmTransfer] = useState(false);
  const toast = useToast();
  const copy = useCopy();

  const save = async () => {
    setBusy(true);
    try {
      const updated = await api.updateRoom(room.roomId, {
        name: name.trim(),
        description: desc.trim(),
        visibility: vis,
        syncMode: sync,
      });
      toast.success("Room updated");
      onChanged({ ...room, ...updated, name, description: desc, visibility: vis, syncMode: sync });
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : "save failed");
    } finally {
      setBusy(false);
    }
  };

  const makeInvite = async () => {
    // Translate the coarse TTL choice into an absolute expiry; capacity "0"
    // means unlimited, which the backend accepts as a large number.
    const ttlDays = { "1d": 1, "7d": 7, "30d": 30 }[inviteTtl];
    const expiresAtMs = Date.now() + ttlDays * 24 * 60 * 60 * 1000;
    const capacity = inviteCap === "0" ? 1_000_000 : Number(inviteCap);
    try {
      const r = await api.createInvite(room.roomId, {
        role: inviteRole,
        capacity,
        expiresAtMs,
      });
      setInvite(r.code);
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : "invite failed");
    }
  };

  const addMember = async () => {
    const id = newMember.trim();
    if (!id) return;
    try {
      await api.addMember(room.roomId, id, newMemberRole);
      toast.success(`Added ${displayNameOf(id)}`);
      setNewMember("");
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : "add member failed");
    }
  };

  const remove = async () => {
    try {
      await api.deleteRoom(room.roomId);
      toast.success("Room deleted");
      onDeleted();
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : "delete failed");
    }
  };

  const transfer = async () => {
    try {
      await api.transferOwner(room.roomId, transferTo.trim());
      toast.success(`Ownership transferred to ${displayNameOf(transferTo.trim())}`);
      setTransferTo("");
      onClose();
    } catch (e) {
      toast.error(e instanceof ApiError ? e.message : "transfer failed");
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title="Room settings"
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
          <Button variant="primary" onClick={save} disabled={busy}>
            {busy ? "Saving…" : "Save changes"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label="Description">
            <Input value={desc} onChange={(e) => setDesc(e.target.value)} />
          </Field>
        </div>

        <Field label="Visibility">
          <Segment
            value={vis}
            onChange={setVis}
            options={[
              { value: "private", label: "Private", hint: "invite only" },
              { value: "shared", label: "Shared", hint: "anyone joins" },
            ]}
          />
        </Field>

        <Field label="Code sync">
          <Segment value={sync} onChange={setSync} options={syncOptions} />
        </Field>
        <p className="-mt-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2 text-xs text-[var(--color-fg-muted)] leading-relaxed">
          {syncGuidance[sync]}
        </p>

        <RoomContentSection roomId={room.roomId} />

        {/* Invite */}
        <div className="rounded-[var(--radius)] border border-[var(--color-border)] p-4">
          <div className="flex items-center justify-between gap-3">
            <div>
              <p className="text-sm font-medium">Invite members</p>
              <p className="text-xs text-[var(--color-fg-muted)]">
                Generate an invite code with a role, expiry, and use limit.
              </p>
            </div>
            <Button variant="outline" size="sm" onClick={makeInvite}>
              Create invite
            </Button>
          </div>
          <div className="mt-3 flex flex-col gap-3">
            <Field label="Role">
              <Segment
                value={inviteRole}
                onChange={setInviteRole}
                options={[
                  { value: "member", label: "Member" },
                  { value: "admin", label: "Admin" },
                  { value: "visitor", label: "Visitor" },
                ]}
              />
            </Field>
            <Field label="Expires">
              <Segment
                value={inviteTtl}
                onChange={setInviteTtl}
                options={[
                  { value: "1d", label: "1 day" },
                  { value: "7d", label: "7 days" },
                  { value: "30d", label: "30 days" },
                ]}
              />
            </Field>
            <Field label="Uses">
              <Segment
                value={inviteCap}
                onChange={setInviteCap}
                options={[
                  { value: "1", label: "1 use" },
                  { value: "5", label: "5" },
                  { value: "25", label: "25" },
                  { value: "0", label: "Unlimited" },
                ]}
              />
            </Field>
          </div>
          {invite && (
            <div className="mt-3 flex items-center gap-2">
              <code className="tabular flex-1 rounded-[var(--radius)] bg-[var(--color-surface-2)] px-3 py-2 text-sm">
                {invite}
              </code>
              <Button
                variant="secondary"
                size="icon"
                onClick={() => void copy(invite, "invite")}
              >
                <CopyIcon size={16} />
              </Button>
            </div>
          )}
        </div>

        {/* Add member by id */}
        <div className="rounded-[var(--radius)] border border-[var(--color-border)] p-4">
          <p className="text-sm font-medium">Add member</p>
          <p className="mb-3 text-xs text-[var(--color-fg-muted)]">
            Add someone directly by their user id, with a role.
          </p>
          <div className="flex flex-col gap-3">
            <Segment
              value={newMemberRole}
              onChange={setNewMemberRole}
              options={[
                { value: "member", label: "Member" },
                { value: "admin", label: "Admin" },
                { value: "visitor", label: "Visitor" },
              ]}
            />
            <div className="flex items-center gap-2">
              <EmailInput
                placeholder="user id"
                value={newMember}
                onChange={(e) => setNewMember(e.target.value)}
              />
              <Button
                variant="secondary"
                onClick={addMember}
                disabled={!newMember.trim()}
              >
                Add
              </Button>
            </div>
          </div>
        </div>

        {/* Transfer ownership */}
        <div className="rounded-[var(--radius)] border border-[var(--color-border)] p-4">
          <p className="text-sm font-medium">Transfer ownership</p>
          <p className="mb-3 text-xs text-[var(--color-fg-muted)]">
            Hand this room to another member. You become a regular member.
          </p>
          <div className="flex items-center gap-2">
            <EmailInput
              placeholder="member id"
              value={transferTo}
              onChange={(e) => setTransferTo(e.target.value)}
            />
            <Button
              variant="secondary"
              onClick={() => transferTo.trim() && setConfirmTransfer(true)}
              disabled={!transferTo.trim()}
            >
              Transfer
            </Button>
          </div>
        </div>

        {/* Danger zone */}
        <div className="flex items-center justify-between rounded-[var(--radius)] border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 p-4">
          <div>
            <p className="text-sm font-medium text-[var(--color-fg)]">
              Delete room
            </p>
            <p className="text-xs text-[var(--color-fg-muted)]">
              Permanently removes this room for everyone.
            </p>
          </div>
          <Button variant="danger" size="sm" onClick={() => setConfirmDelete(true)}>
            <TrashIcon size={14} />
            Delete
          </Button>
        </div>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onClose={() => setConfirmDelete(false)}
        onConfirm={remove}
        title="Delete this room?"
        description="This cannot be undone. All membership and room state is removed."
        confirmLabel="Delete room"
        danger
      />
      <ConfirmDialog
        open={confirmTransfer}
        onClose={() => setConfirmTransfer(false)}
        onConfirm={transfer}
        title={`Transfer ownership to ${displayNameOf(transferTo.trim())}?`}
        description="You will become a regular member of this room."
        confirmLabel="Transfer"
      />
    </Dialog>
  );
}
