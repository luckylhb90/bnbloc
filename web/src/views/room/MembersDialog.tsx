import { useCallback, useEffect, useRef, useState } from "react";
import { Select } from "@/ui/Select";
import { UsersIcon, RobotIcon, XIcon, SignOutIcon, ShieldCheckIcon } from "@phosphor-icons/react";
import { api, type Member } from "@/lib/api";
import { useToast } from "@/ui/Toast";
import { Badge, Skeleton } from "@/ui/Card";
import { dedupeMembers, sameUser } from "@/lib/format";
import { canInvite, isOwner } from "@/lib/roomRoles";
import { ConfirmDialog } from "@/ui/Dialog";
import { useModalLayer } from "@/ui/useModalLayer";
import { agentHandle, type RoomAgent } from "@/views/composer/types";
import { PlatformAgentsSection } from "@/views/room/PlatformAgentsSection";
import type { RoomPlatformAgents } from "@/views/room/useRoomPlatformAgents";
import { agentDisplayLabel } from "@/lib/displayNames";
import { useDisplayNames } from "@/lib/useDisplayName";
import { UserName } from "@/views/UserName";
import { errorText } from "@/lib/errors";

export function MembersDialog({
  roomId,
  online,
  roomAgents,
  platformAgents,
  subject,
  onClose,
}: {
  roomId: string;
  online: string[];
  roomAgents: RoomAgent[];
  platformAgents: Pick<RoomPlatformAgents, "inRoom" | "remove">;
  subject: string | null;
  onClose: () => void;
}) {
  const toast = useToast();
  const [members, setMembers] = useState<Member[] | null>(null);
  useEffect(() => {
    let cancelled = false;
    api
      .members(roomId)
      .then((m) => !cancelled && setMembers(m.members || []))
      .catch(() => !cancelled && setMembers([]));
    return () => {
      cancelled = true;
    };
  }, [roomId]);
  // The drawer is a modal layer: Escape (when top-most), focus trap/restore.
  const surfaceRef = useRef<HTMLDivElement | null>(null);
  useModalLayer(surfaceRef, { onEscape: onClose });
  // Removing a member is destructive (they lose access immediately), so it
  // goes through a confirmation instead of firing on one click.
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);
  const closeConfirm = useCallback(() => setConfirmRemove(null), []);
  // Per-user role mutation in-flight, so the select is disabled while saving.
  const [savingRole, setSavingRole] = useState<Record<string, boolean>>({});
  // The caller may remove others only if they may invite here.
  const me = members?.find((m) => sameUser(m.userId, subject));
  const myRole = me?.role;
  // People render by display name, never by id (PRD 2026-10-10 §1).
  const nameOf = useDisplayNames([...(members ?? []).map((m) => m.userId), ...roomAgents.map((a) => a.ownerId)]);
  const canRemove = !!me && canInvite(me);
  const remove = async (userId: string) => {
    try {
      await api.removeMember(roomId, userId);
      setMembers((cur) => (cur ? cur.filter((m) => !sameUser(m.userId, userId)) : cur));
      toast.success(`Removed ${nameOf(userId)}`);
    } catch (e) {
      toast.error(errorText(e, "Remove failed"));
    }
  };
  const changeRole = async (userId: string, role: "admin" | "member" | "visitor") => {
    setSavingRole((cur) => ({ ...cur, [userId]: true }));
    try {
      await api.setMemberRole(roomId, userId, role);
      setMembers((cur) =>
        cur ? cur.map((m) => (sameUser(m.userId, userId) ? { ...m, role } : m)) : cur,
      );
    } catch (e) {
      toast.error(errorText(e, "Role change failed"));
    } finally {
      setSavingRole((cur) => {
        const next = { ...cur };
        delete next[userId];
        return next;
      });
    }
  };

  const isOnline = (userId: string) => online.some((u) => sameUser(u, userId));
  // De-duplicate case-variant identities, then sort online-first.
  const rows = members
    ? dedupeMembers(members.map((m) => m.userId)).map((userId) => {
        const m = members.find((x) => sameUser(x.userId, userId)) as Member;
        return { ...m, userId };
      })
    : null;
  const sorted = rows
    ? [...rows].sort(
        (a, b) => Number(isOnline(b.userId)) - Number(isOnline(a.userId)),
      )
    : null;
  const onlineCount = rows ? rows.filter((m) => isOnline(m.userId)).length : 0;
  // Platform agents have their own section below.
  const memberAgents = roomAgents.filter((a) => !a.platform);

  return (
    <div className="fixed inset-0 z-50 flex justify-end">
      <div className="absolute inset-0 bg-black/50 animate-in" onClick={onClose} />
      <div
        ref={surfaceRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby="members-dialog-title"
        className="animate-in relative h-full w-80 max-w-full overflow-y-auto border-l border-[var(--color-border-strong)] bg-[var(--color-surface)] p-5"
      >
        <button
          onClick={onClose}
          data-layer-close
          title="Close"
          aria-label="Close members panel"
          className="tactile absolute right-3 top-3 grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
        >
          <XIcon size={18} />
        </button>
        <div className="mb-4 flex items-center gap-2 pr-8">
          <UsersIcon size={18} className="text-[var(--color-fg-muted)]" />
          <h2 id="members-dialog-title" className="font-semibold tracking-tight">
            Members
          </h2>
          {rows && <Badge tone="neutral">{rows.length}</Badge>}
          {onlineCount > 0 && <Badge tone="accent">{onlineCount} online</Badge>}
        </div>
        {sorted === null ? (
          <div className="flex flex-col gap-2">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-10" />
            ))}
          </div>
        ) : (
          <div className="flex flex-col gap-1">
            {sorted.map((m) => {
              const on = isOnline(m.userId);
              return (
                <div
                  key={m.userId}
                  className="flex items-center gap-3 rounded-[var(--radius)] px-2 py-2 hover:bg-[var(--color-surface-2)]"
                >
                  <div className="relative">
                    <div className="grid size-8 place-items-center rounded-full bg-[var(--color-accent-soft)] text-xs font-semibold text-[var(--color-accent)]">
                      {(nameOf(m.userId)[0] || "?").toUpperCase()}
                    </div>
                    <span
                      className={
                        "absolute -bottom-0.5 -right-0.5 size-2.5 rounded-full border-2 border-[var(--color-surface)] " +
                        (on
                          ? "bg-[var(--color-accent)]"
                          : "bg-[var(--color-fg-subtle)]")
                      }
                      title={on ? "Online" : "Offline"}
                    />
                  </div>
                  <UserName id={m.userId} you={sameUser(m.userId, subject)} className="flex-1 text-sm" />
                  {isOwner(m) ? (
                    <Badge tone="accent">owner</Badge>
                  ) : canRemove && !sameUser(m.userId, subject) ? (
                    <Select
                      value={m.role}
                      disabled={!!savingRole[m.userId]}
                      onChange={(e) =>
                        changeRole(
                          m.userId,
                          e.target.value as "admin" | "member" | "visitor",
                        )
                      }
                      className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-1.5 py-0.5 text-xs text-[var(--color-fg-muted)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60 disabled:opacity-50"
                      aria-label={`Change role for ${nameOf(m.userId)}`}
                      title="Change role"
                    >
                      <option value="admin">admin</option>
                      <option value="member">member</option>
                      <option value="visitor">visitor</option>
                    </Select>
                  ) : (
                    m.role && m.role !== "member" && <Badge tone="neutral">{m.role}</Badge>
                  )}
                  {canRemove && !isOwner(m) && !sameUser(m.userId, subject) && (
                    <button
                      onClick={() => setConfirmRemove(m.userId)}
                      title={`Remove ${nameOf(m.userId)}`}
                      aria-label={`Remove ${nameOf(m.userId)} from this room`}
                      className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]"
                    >
                      <SignOutIcon size={14} />
                    </button>
                  )}
                </div>
              );
            })}
          </div>
        )}
        {memberAgents.length > 0 && (
          <div className="mt-6">
            <div className="mb-2 flex items-center gap-2">
              <RobotIcon size={16} className="text-[var(--color-fg-muted)]" />
              <h2 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg-muted)]">
                Agents
              </h2>
              <Badge tone="neutral">{memberAgents.length}</Badge>
            </div>
            <div className="flex flex-col gap-1">
              {memberAgents.map((a) => {
                const handle = agentHandle(a);
                const ownerOnline = isOnline(a.ownerId);
                const borrow = !a.self;
                return (
                  <div
                    key={handle}
                    className="flex items-center gap-3 rounded-[var(--radius)] px-2 py-2 hover:bg-[var(--color-surface-2)]"
                  >
                    <div className="relative">
                      <div className="grid size-8 place-items-center rounded-full bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
                        <RobotIcon size={16} weight="fill" />
                      </div>
                      <span
                        className={
                          "absolute -bottom-0.5 -right-0.5 size-2.5 rounded-full border-2 border-[var(--color-surface)] " +
                          (ownerOnline
                            ? "bg-[var(--color-accent)]"
                            : "bg-[var(--color-fg-subtle)]")
                        }
                        title={ownerOnline ? "Owner online" : "Owner offline"}
                      />
                    </div>
                    <span className="min-w-0 flex-1 truncate text-sm">{agentDisplayLabel(a, nameOf)}</span>
                    {a.sandboxed && (
                      <span
                        title="Sandboxed: when borrowed, this agent runs its commands in its owner's agent sandbox"
                        aria-label="Sandboxed"
                        className="text-[var(--color-accent)]"
                      >
                        <ShieldCheckIcon size={14} weight="fill" />
                      </span>
                    )}
                    {borrow && <Badge tone="neutral">Borrow</Badge>}
                  </div>
                );
              })}
            </div>
          </div>
        )}
        <PlatformAgentsSection
          agents={platformAgents.inRoom}
          myRole={myRole}
          subject={subject}
          onRemove={platformAgents.remove}
        />
      </div>
      <ConfirmDialog
        open={confirmRemove !== null}
        onClose={closeConfirm}
        onConfirm={() => {
          if (confirmRemove) void remove(confirmRemove);
        }}
        title={`Remove ${confirmRemove ? nameOf(confirmRemove) : ""}?`}
        description="They lose access to this room right away. They can rejoin only if the room is shared or they get a new invite."
        confirmLabel="Remove member"
        danger
      />
    </div>
  );
}
