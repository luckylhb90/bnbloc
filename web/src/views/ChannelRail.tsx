import { useMemo, useState } from "react";
import { ArchiveIcon, CaretDownIcon, CaretRightIcon, CompassIcon, FolderSimpleIcon, HashIcon, PlusIcon, StarIcon } from "@phosphor-icons/react";
import type { Channel } from "@/lib/api";
import { foldedChannels, mayCreateSection, mayPlaceChannel, railGroupKey, railGroups, sectionJoin, sectionOf, sharedOrder } from "@/lib/channelRules";
import { foldedSections, setFoldedSections } from "@/lib/roomMemory";
import type { RoomMembership } from "@/lib/roomRoles";
import type { ChannelCreationPolicy, RoomSection } from "@/lib/wire.gen";
import { cn } from "@/lib/cn";
import { ChannelMenu, GroupMenu, SectionMenu, type ChannelActions } from "@/views/room/ChannelMenu";

// ChannelRail is the in-room left rail (docs/prd/2026-10-28-channels-and-
// threads.md §2.1b, C18, C24): the channels the member joined, starred first,
// the top level, then the member's own groups (only they see them), then the
// room's open sections; a member folds a group or a section for themselves.
// Each channel has its unread badge and, on hover, a menu
// of what the member may do with it; "Browse channels" opens the directory
// (every open channel, to join), and the archived channels are one click
// away. Selecting a channel re-scopes the feed; the parent threads the id
// into history, the stream, sends and read cursors.
export function ChannelRail({
  roomId,
  subject,
  me,
  policy,
  active,
  channels,
  sections,
  archivedCount,
  loadFailed,
  reload,
  onSelect,
  unreadByChannel,
  collapsed = false,
  actions,
}: {
  roomId: string;
  subject: string | null;
  // The member's membership (null: a preview, or not known yet): what the
  // menus offer is mirrored from it (lib/channelRules).
  me: RoomMembership | null;
  policy: ChannelCreationPolicy | "";
  active: string | null;
  // The room's channel list and its loader live in the room
  // (useRoomChannels): the rail only renders them.
  channels: Channel[] | null;
  sections: readonly RoomSection[];
  archivedCount: number;
  loadFailed: boolean;
  reload: () => Promise<void>;
  onSelect: (channelId: string) => void;
  // Unread count per channel id. Without it a member sees the room badge but
  // cannot tell which channel it came from.
  unreadByChannel?: Record<string, number>;
  // Icon-only mode: when a preview dock is open the chat column needs every
  // pixel it can get, so the rail sheds its labels (Slack's thread-panel
  // pattern). Active/unread state stays readable on the icon alone.
  collapsed?: boolean;
  actions: ChannelActions;
}) {
  const [folded, setFolded] = useState<string[]>(() => foldedSections(subject, roomId));
  const toggleFold = (id: string) => {
    const next = folded.includes(id) ? folded.filter((x) => x !== id) : [...folded, id];
    setFolded(next);
    setFoldedSections(subject, roomId, next);
  };
  const groups = useMemo(
    () => railGroups(channels ?? [], sections, { active, groups: actions.groups, keepEmpty: (s) => !!me && (mayPlaceChannel(me, subject, policy, s) || sectionJoin(me, s) !== null) }),
    [channels, sections, active, me, subject, policy, actions.groups],
  );
  const canCreateTop = !!me && mayPlaceChannel(me, subject, policy, null);
  const canSection = !!me && mayCreateSection(me, policy);

  const row = (c: Channel, group: RoomSection | null) => {
    const unread = unreadByChannel?.[c.channelId] ?? 0;
    // The active channel is being read right now, so its badge would flicker
    // as messages arrive and are immediately marked read.
    const showBadge = unread > 0 && c.channelId !== active;
    const title = c.purpose ? `${c.name}: ${c.purpose}` : c.isDefault ? "The default channel" : c.name;
    if (collapsed) {
      return (
        <button
          key={c.channelId}
          onClick={() => onSelect(c.channelId)}
          title={title}
          className={cn(
            "tap tactile relative flex h-9 w-9 items-center justify-center self-center rounded-[var(--radius)]",
            c.channelId === active ? "bg-[var(--color-elevated)] text-[var(--color-fg)]" : "text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
          )}
        >
          <HashIcon size={16} />
          {showBadge && <span className="absolute -right-0.5 -top-0.5 h-2.5 w-2.5 rounded-full bg-[var(--color-accent)]" />}
        </button>
      );
    }
    return (
      <div
        key={c.channelId}
        data-channel-row={c.name}
        className={cn(
          "group relative flex items-center rounded-[var(--radius)]",
          c.channelId === active ? "bg-[var(--color-elevated)]" : "hover:bg-[var(--color-surface-2)]",
        )}
      >
        <button
          onClick={() => onSelect(c.channelId)}
          title={title}
          className={cn(
            "tactile flex min-w-0 flex-1 items-center gap-2 px-2 py-1.5 text-sm",
            c.channelId === active ? "font-medium text-[var(--color-fg)]" : showBadge ? "font-medium text-[var(--color-fg)]" : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
          )}
        >
          {c.starred ? <StarIcon size={14} weight="fill" className="shrink-0 text-[var(--color-warning)]" aria-label="Starred" /> : <HashIcon size={14} className="shrink-0 opacity-70" />}
          <span className={cn("flex-1 truncate text-left", !c.joined && "italic")}>{c.name}</span>
          {showBadge && (
            <span className="shrink-0 rounded-full bg-[var(--color-accent)] px-1.5 text-[11px] font-semibold leading-5 text-[var(--color-accent-fg,#04120c)]">
              {unread > 99 ? "99+" : unread}
            </span>
          )}
        </button>
        {me && <ChannelMenu channel={c} section={group} siblings={sharedOrder(channels ?? [], group?.sectionId ?? "")} actions={actions} />}
      </div>
    );
  };

  return (
    <div
      className={
        collapsed
          ? "flex w-12 shrink-0 flex-col gap-0.5 overflow-y-auto overflow-x-hidden border-r border-[var(--color-border)] p-1 transition-[width] duration-200 ease-out"
          : "flex w-44 shrink-0 flex-col gap-0.5 overflow-y-auto overflow-x-hidden border-r border-[var(--color-border)] p-2 transition-[width] duration-200 ease-out"
      }
    >
      {loadFailed && (
        <button type="button" onClick={() => void reload()} className="px-2 py-1 text-left text-xs text-[var(--color-danger)] hover:underline" title="Could not load channels — retry">
          {collapsed ? "!" : "Couldn’t load channels — retry"}
        </button>
      )}
      {channels === null ? (
        <span className="px-2 py-1 text-xs text-[var(--color-fg-subtle)]">…</span>
      ) : (
        groups.map((g) => {
          const id = railGroupKey(g);
          const header = g.group ?? g.section;
          const isFolded = !!header && folded.includes(id);
          const visible = foldedChannels(g.channels, isFolded, active, unreadByChannel);
          // A channel in one of the member's groups keeps its section's
          // menu (moving, settings): the group is only where it sits.
          const sectionFor = (c: Channel) => (g.group ? sectionOf(c, sections) : g.section);
          const name = g.group?.name ?? g.section?.name ?? "";
          const title = g.group ? `${name}: your own group, only you see it` : g.section?.rules ? `${name}: ${g.section.rules}` : name;
          return (
            <div
              key={id || "top"}
              data-rail-section={g.section?.name ?? ""}
              data-rail-group={g.group?.name ?? undefined}
              className="flex flex-col gap-0.5"
            >
              {header && !collapsed && (
                <div className="group mt-2 flex items-center">
                  <button
                    type="button"
                    onClick={() => toggleFold(id)}
                    aria-expanded={!isFolded}
                    title={title}
                    className="tactile flex min-w-0 flex-1 items-center gap-1 px-1 py-1 text-[0.7rem] font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
                  >
                    {isFolded ? <CaretRightIcon size={11} /> : <CaretDownIcon size={11} />}
                    {g.group && <FolderSimpleIcon size={11} aria-hidden="true" className="shrink-0" />}
                    <span className="truncate">{name}</span>
                  </button>
                  {me && g.group && <GroupMenu group={g.group} actions={actions} />}
                  {me && g.section && <SectionMenu section={g.section} actions={actions} />}
                </div>
              )}
              {header && collapsed && <div className="my-1 h-px bg-[var(--color-border)]" aria-hidden="true" />}
              {visible.map((c) => row(c, sectionFor(c)))}
              {g.group && !collapsed && !isFolded && g.channels.length === 0 && (
                <span className="px-2 py-1 text-xs text-[var(--color-fg-subtle)]">Empty: use a channel’s menu to move it here</span>
              )}
            </div>
          );
        })
      )}
      {!collapsed && me && (
        <div className="mt-2 flex flex-col gap-0.5 border-t border-[var(--color-border)] pt-2">
          {canCreateTop || sections.some((s) => mayPlaceChannel(me, subject, policy, s)) ? (
            <RailButton icon={PlusIcon} label="New channel" onClick={actions.create} />
          ) : null}
          {canSection && <RailButton icon={PlusIcon} label="New section" onClick={actions.createSection} />}
          <RailButton icon={CompassIcon} label="Browse channels" onClick={actions.browse} />
          {archivedCount > 0 && <RailButton icon={ArchiveIcon} label={`Archived (${archivedCount})`} onClick={actions.showArchived} />}
        </div>
      )}
      {collapsed && me && (
        <button onClick={actions.browse} title="Browse channels" className="tap tactile mt-1 flex h-9 w-9 items-center justify-center self-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]">
          <CompassIcon size={16} />
        </button>
      )}
    </div>
  );
}

function RailButton({ icon: Icon, label, onClick }: { readonly icon: React.ComponentType<{ size?: number }>; readonly label: string; readonly onClick: () => void }) {
  return (
    <button onClick={onClick} title={label} className="tactile flex min-w-0 items-center gap-2 rounded-[var(--radius)] px-2 py-1.5 text-sm text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]">
      <Icon size={14} /> <span className="truncate">{label}</span>
    </button>
  );
}
