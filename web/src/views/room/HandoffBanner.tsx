import { useEffect, useState } from "react";
import { PackageIcon, XIcon } from "@phosphor-icons/react";
import { api } from "@/lib/api";
import {
  handoffBannerText,
  pendingSuggestions,
  suggestedInviteRole,
  type HandoffMember,
  type RoomHandoffInfo,
} from "@/lib/handoff";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { useToast } from "@/ui/Toast";
import { displayNameOf } from "@/lib/useDisplayName";
import { UserWithEmail } from "@/views/UserName";
import { errorText } from "@/lib/errors";

const dismissKey = (roomId: string) => `bnbloc.handoffBanner.${roomId}`;

// HandoffBanner shows, in a room created from a handoff package, where the
// history came from and — for the owner/admins, who may add members — offers
// the source room's member list as invite suggestions, added through the
// existing add-member flow. Rooms that were not imported render nothing.
export function HandoffBanner({
  roomId,
  canInvite,
  members,
}: {
  readonly roomId: string;
  readonly canInvite: boolean;
  readonly members: readonly string[];
}) {
  const [info, setInfo] = useState<RoomHandoffInfo | null>(null);
  const [dismissed, setDismissed] = useState(() => localStorage.getItem(dismissKey(roomId)) === "1");
  const [inviting, setInviting] = useState(false);
  const [added, setAdded] = useState<string[]>([]);
  const toast = useToast();

  useEffect(() => {
    let live = true;
    api
      .roomHandoff(roomId)
      .then((i) => live && setInfo(i))
      // 404 = not an imported room; any other failure just hides the banner.
      .catch(() => live && setInfo(null));
    return () => {
      live = false;
    };
  }, [roomId]);

  if (!info || dismissed) return null;
  const pending = pendingSuggestions(info.suggestedMembers, [...members, ...added]);
  const dismiss = () => {
    localStorage.setItem(dismissKey(roomId), "1");
    setDismissed(true);
  };
  const add = async (m: HandoffMember) => {
    try {
      await api.addMember(roomId, m.userId, suggestedInviteRole(m.role));
      setAdded((a) => [...a, m.userId]);
      toast.success(`Added ${displayNameOf(m.userId)}`);
    } catch (e) {
      toast.error(errorText(e, "Add member failed"));
    }
  };

  return (
    <>
      <div
        role="status"
        data-handoff-banner
        className="flex flex-wrap items-center gap-3 border-b border-[var(--color-border)] bg-[var(--color-surface-2)] px-4 py-2 text-sm"
      >
        <PackageIcon size={18} className="shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <span className="min-w-0 flex-1 text-[var(--color-fg-muted)]">
          {handoffBannerText(info)}
          {canInvite && pending.length > 0 ? " — invite original members?" : "."}
        </span>
        <div className="flex items-center gap-2">
          {canInvite && pending.length > 0 && (
            <Button size="sm" variant="primary" onClick={() => setInviting(true)}>
              Suggested members ({pending.length})
            </Button>
          )}
          <button
            type="button"
            onClick={dismiss}
            aria-label="Dismiss import notice"
            title="Dismiss"
            className="tactile grid size-7 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
          >
            <XIcon size={14} />
          </button>
        </div>
      </div>
      <Dialog
        open={inviting}
        onClose={() => setInviting(false)}
        title="Invite original members"
        description={`Members of ${info.sourceRoomName || info.sourceRoomId} when it was exported. Nobody gets access until you add them.`}
        footer={
          <Button variant="ghost" onClick={() => setInviting(false)}>
            Close
          </Button>
        }
      >
        {pending.length === 0 ? (
          <p className="text-sm text-[var(--color-fg-muted)]">Everyone suggested is already in this room.</p>
        ) : (
          <ul className="flex flex-col divide-y divide-[var(--color-border)]">
            {pending.map((m) => (
              <li key={m.userId} className="flex items-center gap-3 py-2">
                <UserWithEmail id={m.userId} className="flex-1 text-sm" />
                <span className="text-xs text-[var(--color-fg-subtle)]">
                  was {m.role} · joins as {suggestedInviteRole(m.role)}
                </span>
                <Button size="sm" variant="outline" onClick={() => void add(m)}>
                  Add
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Dialog>
    </>
  );
}
