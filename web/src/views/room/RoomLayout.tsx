import { useCallback, useRef, useState, type ReactNode } from "react";
import { ChatsCircleIcon, FilesIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { ChannelRail } from "@/views/ChannelRail";
import type { RoomChannels } from "@/views/room/useRoomChannels";
import { LazyPreviewWorkbench } from "@/views/workbench/LazyWorkbench";
import type { Workbench } from "@/views/workbench/useWorkbench";
import type { DockStyle } from "@/lib/useDock";
import { useModalLayer } from "@/ui/useModalLayer";

export type RailSurface = "inline" | "drawer";
export type DismissibleLayerSurfaceProps = React.HTMLAttributes<HTMLDivElement> & {
  readonly "data-room-dock"?: string;
  readonly "data-room-dock-mode"?: string;
};

export function DismissibleLayer({
  label,
  closeLabel,
  onClose,
  surfaceClassName,
  surfaceProps,
  children,
}: {
  readonly label: string;
  readonly closeLabel: string;
  readonly onClose: () => void;
  readonly surfaceClassName: string;
  readonly surfaceProps?: DismissibleLayerSurfaceProps;
  readonly children: ReactNode;
}) {
  const panelRef = useRef<HTMLDivElement | null>(null);
  useModalLayer(panelRef, { onEscape: onClose });

  return (
    <div className="fixed inset-0 z-40 flex">
      <button
        type="button"
        aria-label={closeLabel}
        tabIndex={-1}
        className="absolute inset-0 bg-black/60 animate-in"
        onClick={onClose}
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
        {...surfaceProps}
        className={cn(
          "animate-in relative min-h-0 border-[var(--color-border-strong)] bg-[var(--color-surface)] shadow-[var(--shadow-diffuse)] focus:outline-none",
          surfaceProps?.className,
          surfaceClassName,
        )}
      >
        {children}
      </div>
    </div>
  );
}

export function RoomRail({
  roomId,
  activeChannel,
  channels,
  unreadByChannel,
  panel,
  filesBadge,
  collapsed,
  surface,
  onSelectChannel,
  onSelectPanel,
}: {
  readonly roomId: string;
  readonly activeChannel: string | null;
  readonly channels: RoomChannels;
  readonly unreadByChannel: Record<string, number>;
  readonly panel: "chat" | "artifacts";
  readonly filesBadge: number;
  readonly collapsed: boolean;
  readonly surface: RailSurface;
  readonly onSelectChannel: (channelId: string) => void;
  readonly onSelectPanel: (panel: "chat" | "artifacts") => void;
}) {
  return (
    <div
      data-room-rail
      data-room-rail-mode={surface === "drawer" ? "drawer" : collapsed ? "icons" : "full"}
      className={cn(
        "flex shrink-0 flex-col border-r border-[var(--color-border)] transition-[width] duration-200 ease-out",
        surface === "drawer" ? "h-full w-44 max-w-[82vw]" : collapsed ? "w-12" : "w-44",
      )}
    >
      <div className={cn("flex p-2", collapsed ? "flex-col gap-1" : "gap-1")}>
        <button
          type="button"
          onClick={() => onSelectPanel("chat")}
          className={cn(
            "tap tactile flex items-center justify-center rounded-[var(--radius)] text-xs",
            collapsed ? "h-8 w-8 shrink-0" : "flex-1 gap-1 px-2 py-1.5",
            panel === "chat"
              ? "bg-[var(--color-elevated)] font-medium text-[var(--color-fg)]"
              : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)]",
          )}
          title="Conversation"
        >
          <ChatsCircleIcon size={collapsed ? 16 : 14} />
          {!collapsed && "Chat"}
        </button>
        <button
          type="button"
          onClick={() => onSelectPanel("artifacts")}
          data-tour="files"
          className={cn(
            "tap tactile relative flex items-center justify-center rounded-[var(--radius)] text-xs",
            collapsed ? "h-8 w-8 shrink-0" : "flex-1 gap-1 px-2 py-1.5",
            panel === "artifacts"
              ? "bg-[var(--color-elevated)] font-medium text-[var(--color-fg)]"
              : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)]",
          )}
          title="Everything this room produced: documents, code, images, data and web apps"
        >
          <FilesIcon size={collapsed ? 16 : 14} />
          {!collapsed && "Files"}
          {filesBadge > 0 && panel !== "artifacts" && (
            <span className="absolute -right-0.5 -top-0.5 rounded-full bg-[var(--color-accent)] px-1 text-[10px] font-semibold leading-4 text-[var(--color-accent-fg,#04120c)]">
              {filesBadge > 9 ? "9+" : filesBadge}
            </span>
          )}
        </button>
      </div>
      {panel === "chat" && (
        <ChannelRail
          roomId={roomId}
          active={activeChannel}
          channels={channels.channels}
          loadFailed={channels.loadFailed}
          reload={channels.reload}
          onSelect={onSelectChannel}
          unreadByChannel={unreadByChannel}
          collapsed={collapsed}
        />
      )}
    </div>
  );
}

export function PreviewDock({
  mode,
  roomId,
  actor,
  channelId,
  refreshSignal,
  workbench,
  dockStyle,
  draggingDock,
  liveStates,
  onLocalState,
  onClose,
  onPopOut,
  onStartDrag,
}: {
  readonly mode: "inline" | "overlay" | "full";
  readonly roomId: string;
  readonly actor?: string;
  readonly channelId: string | null;
  readonly refreshSignal: number;
  readonly workbench: Workbench;
  readonly dockStyle: DockStyle;
  readonly draggingDock: boolean;
  readonly liveStates: Record<string, { state: unknown; actor: string; nonce: number }>;
  readonly onLocalState: (artifactId: string, state: unknown) => void;
  readonly onClose: () => void;
  readonly onPopOut?: (artifactId: string) => Promise<boolean>;
  readonly onStartDrag: (e: React.PointerEvent) => void;
}) {
  // In-app fullscreen: the same element grows to cover the window (no
  // remount, so a running preview keeps its state); Escape exits through the
  // modal layer stack before it would close an overlay dock.
  const [fullscreen, setFullscreen] = useState(false);
  const exitFullscreen = useCallback(() => setFullscreen(false), []);
  const fullscreenRef = useRef<HTMLDivElement | null>(null);
  useModalLayer(fullscreenRef, { open: fullscreen, onEscape: exitFullscreen });
  const content = (
    <div
      ref={fullscreenRef}
      tabIndex={-1}
      data-workbench-fullscreen={fullscreen || undefined}
      className={cn(
        "flex min-h-0 flex-1 flex-col focus:outline-none",
        fullscreen && "fixed inset-0 z-50 bg-[var(--color-surface)]",
      )}
    >
      <LazyPreviewWorkbench
        roomId={roomId}
        actor={actor}
        channelId={channelId}
        workbench={workbench}
        refreshSignal={refreshSignal}
        liveStates={liveStates}
        onLocalState={onLocalState}
        onClose={onClose}
        onPopOut={onPopOut}
        fullscreen={fullscreen}
        onToggleFullscreen={() => setFullscreen((f) => !f)}
      />
    </div>
  );

  if (mode !== "inline") {
    return (
      <DismissibleLayer
        label="Preview"
        closeLabel="Close preview"
        onClose={onClose}
        surfaceClassName={cn(
          "ml-auto flex flex-col overflow-hidden border-l",
          mode === "full" ? "h-full w-full" : "my-4 mr-4 h-[calc(100%-2rem)] w-[min(42rem,calc(100%-2rem))] rounded-[var(--radius-xl)] border shadow-[var(--shadow-diffuse)]",
        )}
        surfaceProps={{ "data-room-dock": "", "data-room-dock-mode": mode }}
      >
        {content}
      </DismissibleLayer>
    );
  }

  return (
    <>
      <div
        onPointerDown={onStartDrag}
        className="group relative z-10 w-1.5 shrink-0 cursor-col-resize"
        title="Drag to resize the preview"
      >
        <div className="absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-[var(--color-border)] transition-colors group-hover:bg-[var(--color-accent)] group-active:bg-[var(--color-accent)]" />
      </div>
      <aside
        data-room-dock
        data-room-dock-mode="inline"
        className={cn(
          "flex w-[var(--dock-w)] min-w-0 shrink flex-col overflow-hidden border-l border-[var(--color-border)] bg-[var(--color-surface)]",
          draggingDock ? "" : "transition-[width] duration-200 ease-[cubic-bezier(0.16,1,0.3,1)]",
        )}
        style={dockStyle}
      >
        {content}
      </aside>
    </>
  );
}
