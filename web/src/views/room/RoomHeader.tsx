import {
  ArrowLeftIcon,
  GearSixIcon,
  UsersIcon,
  UsersThreeIcon,
  HandshakeIcon,
  ListIcon,
  SignOutIcon,
  MagnifyingGlassIcon,
  XIcon,
  PackageIcon,
  SquaresFourIcon,
  ClockIcon,
  ListChecksIcon,
} from "@phosphor-icons/react";
import type { Room } from "@/lib/api";
import type { Stage } from "@/lib/capability";
import { Button } from "@/ui/Button";
import { cn } from "@/lib/cn";
import { Dot } from "@/ui/Card";
import { formatBytes } from "@/lib/format";
import { canInvite, isOwner, type RoomMembership } from "@/lib/roomRoles";
import { OnlineStrip } from "@/views/room/RoomNav";
import { RoomMuteButton } from "@/views/room/RoomMuteButton";
import type { RailSurface } from "@/views/room/RoomLayout";
import { PlatformAgentsIndicator } from "@/views/room/PlatformAgentsIndicator";
import { RoomSourcesIndicator } from "@/views/room/RoomSourcesIndicator";
import type { RoomPlatformAgent } from "@/lib/platformAgents";

export function RoomHeader({
  room,
  roomId,
  me,
  roomStage,
  railSurface,
  connected,
  showReconnecting,
  online,
  onOpenRailDrawer,
  onBack,
  onToggleSearch,
  onShowBorrow,
  onShowMembers,
  onShowSettings,
  onConfirmLeave,
  onExportHandoff,
  exportingBytes,
  borrowBadge = 0,
  onSaveComponent,
  onShowSchedules,
  onShowRuns,
  onTogglePeopleChat,
  peopleChatOpen = false,
  peopleChatUnread = 0,
  platformAgents = [],
}: {
  readonly room: Room | null;
  readonly roomId: string;
  // The signed-in member's membership (null: not a member, a preview).
  readonly me: RoomMembership | null;
  readonly roomStage: Stage;
  readonly railSurface: RailSurface;
  readonly connected: boolean;
  readonly showReconnecting: boolean;
  readonly online: string[];
  readonly onOpenRailDrawer: () => void;
  readonly onBack: () => void;
  readonly onToggleSearch: () => void;
  readonly onShowBorrow: () => void;
  readonly onShowMembers: () => void;
  readonly onShowSettings: () => void;
  readonly onConfirmLeave: () => void;
  // Owner/admin only: download the room's handoff package. exportingBytes is
  // the running download size while an export is in flight (null otherwise).
  readonly onExportHandoff?: () => void;
  readonly exportingBytes?: number | null;
  // Pending borrow quota requests awaiting this member (F3), shown on the
  // borrow-authorization icon.
  readonly borrowBadge?: number;
  // Owner/admin only: save the room as a room component.
  readonly onSaveComponent?: () => void;
  // Opens the room's scheduled prompts panel (N2).
  readonly onShowSchedules?: () => void;
  // Opens the room's agent run records (R10).
  readonly onShowRuns?: () => void;
  // Opens/closes the room's people chat (absent for a room preview).
  readonly onTogglePeopleChat?: () => void;
  readonly peopleChatOpen?: boolean;
  readonly peopleChatUnread?: number;
  // The room's platform agents: a persistent "can read this room" note.
  readonly platformAgents?: readonly RoomPlatformAgent[];
}) {
  const compact = roomStage === "compact";
  return (
    <div data-tour="room-header" className={cn("pad-safe pad-safe-t flex items-center gap-2 border-b border-[var(--color-border)] px-4 py-3 [--pad-safe-x:1rem] [--pad-safe-y:0.75rem]", compact && "flex-wrap gap-y-1")}>
      {railSurface === "drawer" && (
        <button
          type="button"
          onClick={onOpenRailDrawer}
          className="tap tactile grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
          aria-label="Open channels and documents"
          title="Open channels and documents"
        >
          <ListIcon size={18} />
        </button>
      )}
      <button
        type="button"
        onClick={onBack}
        className="tap tactile grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
        aria-label="Back to rooms"
        title="Back to rooms"
      >
        <ArrowLeftIcon size={18} />
      </button>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <h1 className="truncate font-semibold tracking-tight">
            {room?.name || roomId}
          </h1>
          <span
            className="flex items-center gap-1"
            title={connected ? "Live" : "Reconnecting"}
          >
            <Dot tone={connected ? "accent" : "muted"} />
            {showReconnecting && (
              <span className="text-xs text-[var(--color-fg-subtle)]">
                Reconnecting…
              </span>
            )}
          </span>
        </div>
        <div className="flex min-w-0 items-center gap-2">
          <p className="min-w-0 truncate text-xs text-[var(--color-fg-subtle)] tabular">
            {roomId}
          </p>
          <PlatformAgentsIndicator agents={platformAgents} />
          <RoomSourcesIndicator roomId={roomId} me={me} />
        </div>
      </div>
      {roomStage !== "compact" && <OnlineStrip online={online} />}
      {/* Compact: the room actions move to their own row under the title
          (scrolling sideways if they still do not fit) so the title keeps
          its width and every action stays reachable. */}
      <div className={compact ? "flex w-full items-center justify-between overflow-x-auto" : "contents"}>
      <Button
        variant="ghost"
        size="icon"
        onClick={onToggleSearch}
        title="Search this room"
        aria-label="Search messages in this room"
        className="tap"
      >
        <MagnifyingGlassIcon size={18} />
      </Button>
      {onTogglePeopleChat && (
        <Button
          variant="ghost"
          size="icon"
          onClick={onTogglePeopleChat}
          aria-pressed={peopleChatOpen}
          title={
            peopleChatUnread > 0
              ? `People chat · ${peopleChatUnread} unread — agents can't see it`
              : "People chat — talk with members; agents can't see it"
          }
          aria-label={peopleChatUnread > 0 ? `People chat, ${peopleChatUnread} unread` : "People chat"}
          className={cn("tap relative", peopleChatOpen && "bg-[var(--color-surface-2)] text-[var(--color-fg)]")}
        >
          <UsersThreeIcon size={18} weight={peopleChatOpen ? "fill" : "regular"} />
          {peopleChatUnread > 0 && !peopleChatOpen && (
            <span className="absolute -right-0.5 -top-0.5 grid min-w-4 place-items-center rounded-full bg-[var(--color-accent)] px-1 text-[10px] font-semibold leading-4 text-[var(--color-accent-fg)] tabular">
              {peopleChatUnread > 9 ? "9+" : peopleChatUnread}
            </span>
          )}
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon"
        onClick={onShowBorrow}
        title={borrowBadge > 0 ? `Authorize borrowing of your agents · ${borrowBadge} quota request${borrowBadge === 1 ? "" : "s"} waiting` : "Authorize borrowing of your agents"}
        aria-label={borrowBadge > 0 ? `Authorize borrowing of your agents, ${borrowBadge} quota requests waiting` : "Authorize borrowing of your agents"}
        className="tap relative"
      >
        <HandshakeIcon size={18} />
        {borrowBadge > 0 && (
          <span className="absolute -right-0.5 -top-0.5 grid min-w-4 place-items-center rounded-full bg-[var(--color-accent)] px-1 text-[10px] font-semibold leading-4 text-[var(--color-accent-fg)] tabular">
            {borrowBadge > 9 ? "9+" : borrowBadge}
          </span>
        )}
      </Button>
      {onExportHandoff && (
        <Button
          variant="ghost"
          size="icon"
          onClick={onExportHandoff}
          disabled={exportingBytes != null}
          title={exportingBytes != null ? `Exporting… ${formatBytes(exportingBytes)}` : "Export handoff package"}
          aria-label="Export handoff package"
          className="tap"
        >
          <PackageIcon size={18} />
        </Button>
      )}
      {onShowSchedules && (
        <Button variant="ghost" size="icon" title="Schedules" aria-label="Scheduled messages" onClick={onShowSchedules} className="tap">
          <ClockIcon size={18} />
        </Button>
      )}
      {onShowRuns && (
        <Button variant="ghost" size="icon" title="Agent runs" aria-label="Agent runs" onClick={onShowRuns} className="tap">
          <ListChecksIcon size={18} />
        </Button>
      )}
      {onSaveComponent && (
        <Button variant="ghost" size="icon" title="Save as component" aria-label="Save room as component" onClick={onSaveComponent} className="tap">
          <SquaresFourIcon size={18} />
        </Button>
      )}
      <RoomMuteButton roomId={roomId} />
      <Button variant="ghost" size="icon" title="Members" aria-label="Members" onClick={onShowMembers} className="tap">
        <UsersIcon size={18} />
      </Button>
      {me && canInvite(me) && (
        <Button variant="ghost" size="icon" title="Settings" aria-label="Settings" onClick={onShowSettings} className="tap" data-tour="invite">
          <GearSixIcon size={18} />
        </Button>
      )}
      {/* The owner hands the room over first (transfer), then may leave. */}
      {me && !isOwner(me) && (
        <Button variant="ghost" size="icon" title="Leave room" aria-label="Leave room" onClick={onConfirmLeave} className="tap">
          <SignOutIcon size={18} />
        </Button>
      )}
      </div>
    </div>
  );
}

export function RoomSearchBar({
  search,
  query,
  matchCount,
  onSearchChange,
  onClose,
}: {
  readonly search: string;
  readonly query: string;
  readonly matchCount: number;
  readonly onSearchChange: (value: string) => void;
  readonly onClose: () => void;
}) {
  return (
    <div className="pad-safe flex items-center gap-2 border-b border-[var(--color-border)] px-4 py-2 [--pad-safe-x:1rem]">
      <MagnifyingGlassIcon size={16} className="text-[var(--color-fg-subtle)]" />
      <input
        autoFocus
        value={search}
        onChange={(e) => onSearchChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") onClose();
        }}
        placeholder="Search loaded messages…"
        aria-label="Search messages"
        className="min-w-0 flex-1 bg-transparent text-sm text-[var(--color-fg)] placeholder:text-[var(--color-fg-subtle)] focus:outline-none"
      />
      {query && (
        <span className="tabular whitespace-nowrap text-xs text-[var(--color-fg-subtle)]">
          {matchCount} match{matchCount === 1 ? "" : "es"} · older
          history not searched
        </span>
      )}
      <button
        type="button"
        onClick={onClose}
        title="Clear search"
        aria-label="Clear search"
        className="tap tactile grid size-9 place-items-center rounded-[var(--radius)] text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
      >
        <XIcon size={16} />
      </button>
    </div>
  );
}
