import {
  ArchiveIcon,
  ArrowCounterClockwiseIcon,
  ArrowDownIcon,
  ArrowUpIcon,
  DotsThreeVerticalIcon,
  EyeSlashIcon,
  GearIcon,
  SignOutIcon,
  StarIcon,
  TrashIcon,
} from "@phosphor-icons/react";
import type { Channel } from "@/lib/api";
import { mayDeleteChannel, mayManageChannel, mayManageSection } from "@/lib/channelRules";
import type { RoomMembership } from "@/lib/roomRoles";
import type { RoomSection } from "@/lib/wire.gen";
import { Menu, MenuGroup, MenuItem } from "@/ui/Menu";
import { cn } from "@/lib/cn";

/** What the rail's menus do: the room's channel manager
 *  (views/room/useChannelManager.tsx) owns the calls and the dialogs; the
 *  menus only offer what the member may do (lib/channelRules, the server's
 *  rules mirrored). */
export interface ChannelActions {
  readonly me: RoomMembership | null;
  readonly subject: string | null;
  readonly create: () => void;
  readonly createSection: () => void;
  readonly browse: () => void;
  readonly showArchived: () => void;
  readonly settings: (c: Channel) => void;
  readonly move: (c: Channel, siblings: readonly Channel[], step: -1 | 1) => void;
  readonly mark: (c: Channel, marks: { readonly starred?: boolean; readonly hidden?: boolean }) => void;
  readonly leave: (c: Channel) => void;
  readonly archive: (c: Channel, archived: boolean) => void;
  readonly remove: (c: Channel) => void;
  readonly editSection: (s: RoomSection) => void;
  // A channel's last action's outcome, said on its row until the next one
  // (a refusal in the server's words).
  readonly note: (channelId: string) => string;
}

const kebab = (open: boolean) =>
  cn(
    "tactile grid size-7 shrink-0 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] focus-visible:opacity-100",
    open ? "opacity-100" : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 max-[900px]:opacity-100",
  );

// ChannelMenu is a channel's menu in the rail: the member's own order (star,
// hide, leave), and what the channel's managers may do (settings, move,
// archive, delete), each item shown only to who may.
export function ChannelMenu({
  channel: c,
  section,
  siblings,
  actions,
}: {
  readonly channel: Channel;
  readonly section: RoomSection | null;
  readonly siblings: readonly Channel[];
  readonly actions: ChannelActions;
}) {
  const me = actions.me;
  if (!me) return null;
  const manage = mayManageChannel(me, actions.subject, c, section);
  const at = siblings.findIndex((x) => x.channelId === c.channelId);
  const note = actions.note(c.channelId);
  return (
    <>
      <Menu
        label={`#${c.name}`}
        menuClassName="absolute right-0 top-full mt-1 w-56"
        trigger={(props, open) => (
          <button {...props} aria-label={`Channel options for #${c.name}`} title="Channel options" className={kebab(open)}>
            <DotsThreeVerticalIcon size={16} weight="bold" />
          </button>
        )}
      >
        <MenuGroup label="For you">
          <MenuItem icon={StarIcon} onSelect={() => actions.mark(c, { starred: !c.starred })}>
            {c.starred ? "Unstar" : "Star"}
          </MenuItem>
          {!c.isDefault && (
            <MenuItem icon={EyeSlashIcon} onSelect={() => actions.mark(c, { hidden: true })}>
              Hide from my list
            </MenuItem>
          )}
          {!c.isDefault && c.joined && (
            <MenuItem icon={SignOutIcon} onSelect={() => actions.leave(c)}>
              Leave channel
            </MenuItem>
          )}
        </MenuGroup>
        {manage && (
          <MenuGroup label="Channel">
            <MenuItem icon={GearIcon} onSelect={() => actions.settings(c)}>
              Settings, purpose and rules
            </MenuItem>
            {at > 0 && (
              <MenuItem icon={ArrowUpIcon} onSelect={() => actions.move(c, siblings, -1)}>
                Move up
              </MenuItem>
            )}
            {at >= 0 && at < siblings.length - 1 && (
              <MenuItem icon={ArrowDownIcon} onSelect={() => actions.move(c, siblings, 1)}>
                Move down
              </MenuItem>
            )}
            {!c.isDefault && (
              <MenuItem icon={c.archivedAtMs ? ArrowCounterClockwiseIcon : ArchiveIcon} onSelect={() => actions.archive(c, !c.archivedAtMs)}>
                {c.archivedAtMs ? "Restore" : "Archive"}
              </MenuItem>
            )}
            {mayDeleteChannel(me, c) && (
              <MenuItem icon={TrashIcon} onSelect={() => actions.remove(c)}>
                Delete…
              </MenuItem>
            )}
          </MenuGroup>
        )}
      </Menu>
      {note && (
        <p role="status" className="absolute left-2 right-0 top-full z-10 rounded-[var(--radius)] bg-[var(--color-elevated)] px-2 py-1 text-xs text-[var(--color-danger)] shadow-[var(--shadow-pop)]">
          {note}
        </p>
      )}
    </>
  );
}

// SectionMenu is a section's menu: its settings (name, rules, owners) and
// its deletion, for its owners and the room's owner and admins.
export function SectionMenu({ section, actions }: { readonly section: RoomSection; readonly actions: ChannelActions }) {
  const me = actions.me;
  if (!me || !mayManageSection(me, actions.subject, section)) return null;
  return (
    <Menu
      label={section.name}
      menuClassName="absolute right-0 top-full mt-1 w-52"
      trigger={(props, open) => (
        <button {...props} aria-label={`Section options for ${section.name}`} title="Section options" className={kebab(open)}>
          <DotsThreeVerticalIcon size={14} weight="bold" />
        </button>
      )}
    >
      <MenuItem icon={GearIcon} onSelect={() => actions.editSection(section)}>
        Section settings
      </MenuItem>
    </Menu>
  );
}
