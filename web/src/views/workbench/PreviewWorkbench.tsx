import { useRef, useState } from "react";
import { DotsThreeIcon, PushPinIcon, PushPinSlashIcon, SquaresFourIcon, XIcon, XSquareIcon } from "@phosphor-icons/react";
import { fileCategory, versionFacts } from "@/lib/artifactFiles";
import { cn } from "@/lib/cn";
import { DELIVERABLES_TAB, type WorkbenchTab } from "@/lib/workbenchTabs";
import { Menu, MenuItem } from "@/ui/Menu";
import { fileInfoFor, useArtifactDetail, versionMeta } from "./artifactData";
import { ArtifactTabView } from "./ArtifactTabView";
import { DeliverablesView } from "./DeliverablesView";
import { categoryIcons } from "./fileIcons";
import type { Workbench } from "./useWorkbench";

// PreviewWorkbench: the room's right-hand preview — a strip of tabs (many
// deliverables open at once, plus the "All deliverables" list), reorderable
// by drag (or Alt+Shift+Arrow), pinnable, closable — over the active tab.
// Only the active tab is mounted; the others keep their state in the tab
// model (version, viewer) and reload their bytes from cache.

const STRIP_BUTTON =
  "tap tactile grid size-8 shrink-0 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60";

function TabLabel({ roomId, tab }: { readonly roomId: string; readonly tab: WorkbenchTab }) {
  const { detail } = useArtifactDetail(roomId, tab.artifactId ?? null);
  if (tab.key === DELIVERABLES_TAB) {
    return (
      <>
        <SquaresFourIcon size={14} className="shrink-0" aria-hidden />
        <span className="truncate">Deliverables</span>
      </>
    );
  }
  const artifact = detail?.artifact;
  const meta = artifact ? versionMeta(detail, tab.version ?? artifact.currentVersion) : undefined;
  const Icon = categoryIcons[artifact ? fileCategory(versionFacts(artifact, meta)) : "file"];
  return (
    <>
      <Icon size={14} className="shrink-0" aria-hidden />
      <span className="truncate">{artifact ? fileInfoFor(artifact, artifact.currentVersion, meta).name : "…"}</span>
      {tab.version !== undefined && <span className="shrink-0 text-[10px] text-[var(--color-fg-subtle)] tabular">v{tab.version}</span>}
    </>
  );
}

export function PreviewWorkbench({
  roomId,
  actor,
  channelId,
  workbench,
  refreshSignal,
  liveStates,
  onLocalState,
  onClose,
  onPopOut,
  fullscreen,
  onToggleFullscreen,
}: {
  readonly roomId: string;
  readonly actor?: string;
  readonly channelId: string | null;
  readonly workbench: Workbench;
  readonly refreshSignal: number;
  readonly liveStates: Record<string, { state: unknown; actor: string; nonce: number }>;
  readonly onLocalState: (artifactId: string, state: unknown) => void;
  readonly onClose: () => void;
  // Desktop shell: move a tab into its own OS window.
  readonly onPopOut?: (artifactId: string) => Promise<boolean>;
  readonly fullscreen: boolean;
  readonly onToggleFullscreen: () => void;
}) {
  const { state, dispatch } = workbench;
  const active = state.tabs.find((t) => t.key === state.active) ?? state.tabs[0] ?? null;
  const [dragKey, setDragKey] = useState<string | null>(null);
  const [dropKey, setDropKey] = useState<string | null>(null);
  const tabRefs = useRef(new Map<string, HTMLButtonElement>());

  const focusTab = (key: string) => {
    dispatch({ type: "activate", key });
    requestAnimationFrame(() => tabRefs.current.get(key)?.focus());
  };
  const onTabKey = (e: React.KeyboardEvent, index: number, tab: WorkbenchTab) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight" && e.key !== "Delete") return;
    e.preventDefault();
    if (e.key === "Delete") {
      dispatch({ type: "close", key: tab.key });
      return;
    }
    const step = e.key === "ArrowLeft" ? -1 : 1;
    if (e.altKey && e.shiftKey) {
      dispatch({ type: "move", key: tab.key, toIndex: index + step });
      requestAnimationFrame(() => tabRefs.current.get(tab.key)?.focus());
      return;
    }
    const next = state.tabs[(index + step + state.tabs.length) % state.tabs.length];
    if (next) focusTab(next.key);
  };

  return (
    <div className="flex h-full min-h-0 flex-col" data-preview-workbench>
      <div className="pad-safe pad-safe-t flex shrink-0 items-center gap-1 border-b border-[var(--color-border)] px-1.5 py-1 [--pad-safe-x:0.375rem] [--pad-safe-y:0.25rem]">
        <div role="tablist" aria-label="Open deliverables" className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto [scrollbar-width:none]">
          {state.tabs.map((tab, index) => {
            const selected = tab.key === active?.key;
            return (
              <div
                key={tab.key}
                draggable
                onDragStart={(e) => {
                  setDragKey(tab.key);
                  e.dataTransfer.effectAllowed = "move";
                  e.dataTransfer.setData("text/plain", tab.key);
                }}
                onDragOver={(e) => {
                  if (!dragKey) return;
                  e.preventDefault();
                  setDropKey(tab.key);
                }}
                onDragLeave={() => setDropKey((k) => (k === tab.key ? null : k))}
                onDrop={(e) => {
                  e.preventDefault();
                  if (dragKey && dragKey !== tab.key) dispatch({ type: "move", key: dragKey, toIndex: index });
                  setDragKey(null);
                  setDropKey(null);
                }}
                onDragEnd={() => {
                  setDragKey(null);
                  setDropKey(null);
                }}
                className={cn(
                  "group flex h-8 max-w-[13rem] shrink-0 items-center rounded-[var(--radius)] text-xs",
                  selected ? "bg-[var(--color-elevated)] text-[var(--color-fg)]" : "text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)]",
                  dropKey === tab.key && dragKey !== tab.key && "ring-2 ring-inset ring-[var(--color-accent)]/60",
                  dragKey === tab.key && "opacity-50",
                )}
              >
                <button
                  ref={(el) => {
                    if (el) tabRefs.current.set(tab.key, el);
                    else tabRefs.current.delete(tab.key);
                  }}
                  type="button"
                  role="tab"
                  id={`wb-tab-${tab.key}`}
                  aria-selected={selected}
                  aria-controls="wb-panel"
                  tabIndex={selected ? 0 : -1}
                  onClick={() => dispatch({ type: "activate", key: tab.key })}
                  onAuxClick={(e) => {
                    if (e.button === 1) dispatch({ type: "close", key: tab.key });
                  }}
                  onKeyDown={(e) => onTabKey(e, index, tab)}
                  title={tab.pinned ? "Pinned tab" : undefined}
                  className="flex h-full min-w-0 items-center gap-1.5 pl-2 pr-1 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-accent)]/60"
                >
                  {tab.pinned && <PushPinIcon size={11} weight="fill" className="shrink-0 text-[var(--color-accent)]" aria-label="Pinned" />}
                  <TabLabel roomId={roomId} tab={tab} />
                </button>
                {!tab.pinned && (
                  <button
                    type="button"
                    tabIndex={-1}
                    onClick={() => dispatch({ type: "close", key: tab.key })}
                    className={cn(
                      "mr-1 grid size-5 shrink-0 place-items-center rounded text-[var(--color-fg-subtle)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]",
                      !selected && "opacity-0 group-hover:opacity-100 [@media(pointer:coarse)]:opacity-100",
                    )}
                    aria-label="Close tab"
                    title="Close tab"
                  >
                    <XIcon size={11} />
                  </button>
                )}
              </div>
            );
          })}
        </div>
        <button type="button" className={STRIP_BUTTON} onClick={() => workbench.openDeliverables()} title="All deliverables" aria-label="All deliverables">
          <SquaresFourIcon size={16} />
        </button>
        {active && (
          <Menu
            label="Tab actions"
            menuClassName="absolute right-0 top-full mt-1 w-52"
            trigger={(props) => (
              <button {...props} className={STRIP_BUTTON} title="Tab actions" aria-label="Tab actions">
                <DotsThreeIcon size={18} weight="bold" />
              </button>
            )}
          >
            <MenuItem icon={active.pinned ? PushPinSlashIcon : PushPinIcon} onSelect={() => dispatch({ type: "pin", key: active.key, pinned: !active.pinned })}>
              {active.pinned ? "Unpin tab" : "Pin tab"}
            </MenuItem>
            <MenuItem icon={XIcon} onSelect={() => dispatch({ type: "close", key: active.key })}>
              Close tab
            </MenuItem>
            <MenuItem icon={XSquareIcon} onSelect={() => dispatch({ type: "closeOthers", key: active.key })}>
              Close other tabs
            </MenuItem>
          </Menu>
        )}
        <button type="button" className={STRIP_BUTTON} onClick={onClose} title="Close preview" aria-label="Close preview">
          <XIcon size={18} />
        </button>
      </div>
      <div id="wb-panel" role="tabpanel" aria-labelledby={active ? `wb-tab-${active.key}` : undefined} className="min-h-0 flex-1">
        {!active ? null : active.key === DELIVERABLES_TAB ? (
          <DeliverablesView
            roomId={roomId}
            channelId={channelId}
            refreshSignal={refreshSignal}
            sourceSeq={active.filter?.sourceSeq}
            onClearSourceSeq={() => dispatch({ type: "openDeliverables" })}
            onOpen={(id) => workbench.openArtifact(id)}
          />
        ) : active.artifactId ? (
          <ArtifactTabView
            key={active.key}
            roomId={roomId}
            actor={actor}
            artifactId={active.artifactId}
            version={active.version}
            viewerId={active.viewerId}
            onViewer={(viewerId) => dispatch({ type: "setViewer", key: active.key, viewerId })}
            onVersion={(version) => dispatch({ type: "setVersion", key: active.key, version })}
            liveStates={liveStates}
            onLocalState={onLocalState}
            onOpenWindow={
              onPopOut && active.artifactId
                ? () => {
                    const id = active.artifactId!;
                    void onPopOut(id).then((ok) => {
                      if (ok) dispatch({ type: "close", key: active.key });
                    });
                  }
                : undefined
            }
            fullscreen={fullscreen}
            onToggleFullscreen={onToggleFullscreen}
          />
        ) : null}
      </div>
    </div>
  );
}
