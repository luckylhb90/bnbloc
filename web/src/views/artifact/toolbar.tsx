import { useEffect, useRef } from "react";
import { Select } from "@/ui/Select";
import {
  ArrowClockwiseIcon,
  ArrowSquareOutIcon,
  ArrowsInLineVerticalIcon,
  ArrowsOutLineVerticalIcon,
  ArrowUDownLeftIcon,
  CaretDownIcon,
  CaretUpIcon,
  CheckerboardIcon,
  CopyIcon,
  CornersInIcon,
  CornersOutIcon,
  CropIcon,
  ScissorsIcon,
  DeviceMobileIcon,
  EyeIcon,
  FilmStripIcon,
  GridFourIcon,
  InfoIcon,
  ListNumbersIcon,
  MagnifyingGlassMinusIcon,
  MagnifyingGlassPlusIcon,
  PaletteIcon,
  PauseIcon,
  PlayIcon,
  RowsIcon,
  RulerIcon,
  StackIcon,
} from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import type { ViewerToolbarItem, ViewerToolbarSink, ViewerToolbarValue } from "./registry";

// The viewer-contributed part of a workbench tab's toolbar. Viewers publish
// items (button | toggle | zoom | select) by name — never components or
// markup — so a sandboxed previewer component and a built-in viewer drive the
// same host chrome through the same data.

type Icon = typeof EyeIcon;

// Icon names: the previewer frame protocol's code-owned list plus a few the
// built-ins use. Unknown names render the label as text.
const ICONS: Readonly<Record<string, Icon>> = {
  "zoom-in": MagnifyingGlassPlusIcon,
  "zoom-out": MagnifyingGlassMinusIcon,
  fit: CornersInIcon,
  expand: CornersOutIcon,
  grid: GridFourIcon,
  layers: StackIcon,
  background: CheckerboardIcon,
  frames: FilmStripIcon,
  play: PlayIcon,
  pause: PauseIcon,
  reload: ArrowClockwiseIcon,
  eye: EyeIcon,
  ruler: RulerIcon,
  crop: CropIcon,
  cut: ScissorsIcon,
  palette: PaletteIcon,
  info: InfoIcon,
  wrap: ArrowUDownLeftIcon,
  "line-numbers": ListNumbersIcon,
  "page-prev": CaretUpIcon,
  "page-next": CaretDownIcon,
  copy: CopyIcon,
  device: DeviceMobileIcon,
  "expand-all": ArrowsOutLineVerticalIcon,
  "collapse-all": ArrowsInLineVerticalIcon,
  rows: RowsIcon,
  "open-external": ArrowSquareOutIcon,
};

const ZOOM_MIN = 0.01;
const ZOOM_MAX = 64;
const ZOOM_STEP = 1.25;

/** The next zoom factor for a zoom control's in/out step. */
export function stepZoom(value: number, direction: 1 | -1): number {
  const next = direction > 0 ? value * ZOOM_STEP : value / ZOOM_STEP;
  return Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, next));
}

export interface ToolbarHost extends ViewerToolbarSink {
  emit(id: string, value: ViewerToolbarValue): void;
}

/** One viewer instance's sink: items flow to the tab, gestures back. */
export function createToolbarHost(onItems: (items: readonly ViewerToolbarItem[]) => void): ToolbarHost {
  const handlers = new Set<(id: string, value: ViewerToolbarValue) => void>();
  return {
    set: (items) => onItems(items),
    subscribe(handler) {
      handlers.add(handler);
      return () => handlers.delete(handler);
    },
    emit(id, value) {
      for (const handler of handlers) handler(id, value);
    },
  };
}

/** Publishes a built-in viewer's items whenever they change and routes
 *  gestures to the latest handler; clears the items on unmount. */
export function useViewerToolbar(
  sink: ViewerToolbarSink,
  items: readonly ViewerToolbarItem[],
  onAction: (id: string, value: ViewerToolbarValue) => void,
): void {
  const handlerRef = useRef(onAction);
  handlerRef.current = onAction;
  const key = JSON.stringify(items);
  const itemsRef = useRef(items);
  itemsRef.current = items;
  useEffect(() => {
    sink.set(itemsRef.current);
  }, [sink, key]);
  useEffect(() => {
    const off = sink.subscribe((id, value) => handlerRef.current(id, value));
    return () => {
      off();
      sink.set([]);
    };
  }, [sink]);
}

const BUTTON =
  "tap tactile inline-flex h-8 min-w-8 items-center justify-center gap-1 rounded-[var(--radius)] px-1.5 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] disabled:pointer-events-none disabled:opacity-40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60";

function ItemIcon({ name, label }: { readonly name: string; readonly label: string }) {
  const Glyph = ICONS[name];
  return Glyph ? <Glyph size={16} aria-hidden /> : <span className="px-0.5">{label}</span>;
}

export function ViewerToolbar({
  items,
  onAction,
  className,
}: {
  readonly items: readonly ViewerToolbarItem[];
  readonly onAction: (id: string, value: ViewerToolbarValue) => void;
  readonly className?: string;
}) {
  if (items.length === 0) return null;
  return (
    <div role="toolbar" aria-label="Viewer controls" className={cn("flex flex-wrap items-center gap-0.5", className)}>
      {items.map((item) => {
        switch (item.kind) {
          case "button":
            return (
              <button
                key={item.id}
                type="button"
                className={BUTTON}
                disabled={item.disabled}
                title={item.label}
                aria-label={item.label}
                onClick={() => onAction(item.id, null)}
              >
                <ItemIcon name={item.icon} label={item.label} />
              </button>
            );
          case "toggle":
            return (
              <button
                key={item.id}
                type="button"
                className={cn(BUTTON, item.active && "bg-[var(--color-accent-soft)] text-[var(--color-accent)]")}
                disabled={item.disabled}
                aria-pressed={!!item.active}
                title={item.label}
                aria-label={item.label}
                onClick={() => onAction(item.id, !item.active)}
              >
                <ItemIcon name={item.icon} label={item.label} />
              </button>
            );
          case "zoom": {
            const value = item.value ?? 1;
            return (
              <span key={item.id} className="inline-flex items-center" role="group" aria-label={item.label}>
                <button
                  type="button"
                  className={BUTTON}
                  disabled={item.disabled || value <= ZOOM_MIN}
                  title="Zoom out"
                  aria-label="Zoom out"
                  onClick={() => onAction(item.id, stepZoom(value, -1))}
                >
                  <MagnifyingGlassMinusIcon size={16} aria-hidden />
                </button>
                <button
                  type="button"
                  className={cn(BUTTON, "tabular w-14 font-medium")}
                  disabled={item.disabled}
                  title="Reset to 100%"
                  aria-label={`${item.label}: ${Math.round(value * 100)}%, reset to 100%`}
                  onClick={() => onAction(item.id, 1)}
                >
                  {Math.round(value * 100)}%
                </button>
                <button
                  type="button"
                  className={BUTTON}
                  disabled={item.disabled || value >= ZOOM_MAX}
                  title="Zoom in"
                  aria-label="Zoom in"
                  onClick={() => onAction(item.id, stepZoom(value, 1))}
                >
                  <MagnifyingGlassPlusIcon size={16} aria-hidden />
                </button>
              </span>
            );
          }
          case "select":
            return (
              <label key={item.id} className="inline-flex items-center gap-1 text-xs text-[var(--color-fg-muted)]" title={item.label}>
                <ItemIcon name={item.icon} label={item.label} />
                <span className="sr-only">{item.label}</span>
                <Select
                  value={item.selected ?? ""}
                  disabled={item.disabled}
                  onChange={(e) => onAction(item.id, e.target.value)}
                  className="h-8 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-1.5 text-xs text-[var(--color-fg)]"
                >
                  {(item.options ?? []).map((o) => (
                    <option key={o.value} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                </Select>
              </label>
            );
          default:
            return null;
        }
      })}
    </div>
  );
}
