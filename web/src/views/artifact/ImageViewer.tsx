import { useCallback, useEffect, useRef, useState } from "react";
import { cn } from "@/lib/cn";
import { formatBytes } from "@/lib/format";
import type { ArtifactViewerProps } from "./registry";
import { useViewerToolbar } from "./toolbar";
import { centered, clampScale, fitScale, GRID_MIN_SCALE, visibleGrid, zoomAt, type ImageView, type Size } from "./imageView";

// ImageViewer renders raster images (animated GIF/WebP keep animating — the
// <img> element plays them) and SVG, always as an <img> from a blob URL: an
// SVG's scripts never run and its external references never load, and the
// markup never becomes app DOM. Zoom (toolbar, wheel, pinch, double-click),
// pan (drag), fit / 100%, a pixel grid from 400%, and a checkerboard
// background for transparency.

// Older inline image artifacts carry a URL as text instead of bytes.
const LEGACY_URL = /^(https:\/\/|data:image\/(png|jpe?g|gif|webp);base64,)/i;

function useImageSource(blob: Blob): { url: string | null; error: string } {
  const [state, setState] = useState<{ url: string | null; error: string }>({ url: null, error: "" });
  useEffect(() => {
    if (blob.type.startsWith("image/")) {
      const url = URL.createObjectURL(blob);
      setState({ url, error: "" });
      return () => URL.revokeObjectURL(url);
    }
    let cancelled = false;
    void blob.text().then((text) => {
      if (cancelled) return;
      const src = text.trim();
      setState(LEGACY_URL.test(src) ? { url: src, error: "" } : { url: null, error: "Unsupported image source." });
    });
    return () => {
      cancelled = true;
    };
  }, [blob]);
  return state;
}

const CHECKER =
  "repeating-conic-gradient(color-mix(in srgb, var(--color-fg) 9%, transparent) 0% 25%, transparent 0% 50%) 0 0 / 16px 16px";

export default function ImageViewer({ blob, file, toolbar }: ArtifactViewerProps) {
  const { url, error } = useImageSource(blob);
  const boxRef = useRef<HTMLDivElement | null>(null);
  const [box, setBox] = useState<Size>({ w: 0, h: 0 });
  const [natural, setNatural] = useState<Size | null>(null);
  const [view, setView] = useState<ImageView>({ scale: 1, x: 0, y: 0 });
  const [fit, setFit] = useState(true);
  const [grid, setGrid] = useState(false);
  const [checker, setChecker] = useState(true);
  const [failed, setFailed] = useState(false);
  const viewRef = useRef(view);
  viewRef.current = view;

  useEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => {
      if (entry) setBox({ w: entry.contentRect.width, h: entry.contentRect.height });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Fit mode follows the container (dock resize, fullscreen).
  useEffect(() => {
    if (fit && natural && box.w > 0) setView(centered(natural, box, fitScale(natural, box)));
  }, [fit, natural, box]);

  const zoomTo = useCallback(
    (scale: number, px?: number, py?: number) => {
      setFit(false);
      setView((v) => zoomAt(v, clampScale(scale), px ?? box.w / 2, py ?? box.h / 2));
    },
    [box],
  );
  const actualSize = useCallback(() => {
    if (!natural) return;
    setFit(false);
    setView(centered(natural, box, 1));
  }, [natural, box]);

  // Wheel zoom around the pointer (non-passive: the page must not scroll).
  useEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const rect = el.getBoundingClientRect();
      const delta = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY;
      zoomTo(viewRef.current.scale * Math.exp(-delta * 0.0015), e.clientX - rect.left, e.clientY - rect.top);
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [zoomTo]);

  // Pan with one pointer, pinch-zoom with two.
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0 && e.pointerType === "mouse") return;
    e.currentTarget.setPointerCapture(e.pointerId);
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
  };
  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const map = pointers.current;
    const prev = map.get(e.pointerId);
    if (!prev) return;
    const rect = e.currentTarget.getBoundingClientRect();
    if (map.size === 1) {
      setFit(false);
      setView((v) => ({ ...v, x: v.x + e.clientX - prev.x, y: v.y + e.clientY - prev.y }));
    } else if (map.size === 2) {
      const other = [...map.entries()].find(([id]) => id !== e.pointerId)?.[1];
      if (other) {
        const before = Math.hypot(prev.x - other.x, prev.y - other.y);
        const after = Math.hypot(e.clientX - other.x, e.clientY - other.y);
        if (before > 0) {
          const mx = (e.clientX + other.x) / 2 - rect.left;
          const my = (e.clientY + other.y) / 2 - rect.top;
          zoomTo(viewRef.current.scale * (after / before), mx, my);
        }
      }
    }
    map.set(e.pointerId, { x: e.clientX, y: e.clientY });
  };
  const onPointerUp = (e: React.PointerEvent<HTMLDivElement>) => {
    pointers.current.delete(e.pointerId);
  };
  const onDoubleClick = (e: React.MouseEvent<HTMLDivElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (fit || Math.abs(view.scale - 1) > 0.001) zoomTo(Math.max(1, view.scale * 2), e.clientX - rect.left, e.clientY - rect.top);
    else setFit(true);
  };
  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "+" || e.key === "=") zoomTo(view.scale * 1.25);
    else if (e.key === "-") zoomTo(view.scale / 1.25);
    else if (e.key === "0") actualSize();
    else if (e.key.toLowerCase() === "f") setFit(true);
    else return;
    e.preventDefault();
  };

  const gridAvailable = view.scale >= GRID_MIN_SCALE;
  useViewerToolbar(
    toolbar,
    [
      { id: "zoom", kind: "zoom", icon: "zoom-in", label: "Zoom", value: view.scale },
      { id: "fit", kind: "button", icon: "fit", label: "Fit to view", disabled: fit },
      { id: "actual", kind: "button", icon: "actual-size", label: "1:1" },
      { id: "grid", kind: "toggle", icon: "grid", label: "Pixel grid (from 400%)", active: grid && gridAvailable, disabled: !gridAvailable },
      { id: "background", kind: "toggle", icon: "background", label: "Checkerboard background", active: checker },
    ],
    (id, value) => {
      if (id === "zoom" && typeof value === "number") zoomTo(value);
      else if (id === "fit") setFit(true);
      else if (id === "actual") actualSize();
      else if (id === "grid") setGrid(value === true);
      else if (id === "background") setChecker(value === true);
    },
  );

  const overlay = grid && gridAvailable && natural ? visibleGrid(view, natural, box) : null;
  const message = error || (failed ? "Could not decode this image." : "");
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div
        ref={boxRef}
        tabIndex={0}
        role="img"
        aria-label={file.name}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
        onDoubleClick={onDoubleClick}
        onKeyDown={onKeyDown}
        className="relative min-h-0 flex-1 cursor-grab touch-none select-none overflow-hidden outline-none active:cursor-grabbing focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-accent)]/60"
        style={{ background: checker ? CHECKER : undefined }}
      >
        {message ? (
          <p className="p-4 text-sm text-[var(--color-fg-subtle)]">{message}</p>
        ) : url ? (
          <img
            src={url}
            alt=""
            draggable={false}
            onLoad={(e) => {
              const img = e.currentTarget;
              setNatural({ w: img.naturalWidth || 300, h: img.naturalHeight || 150 });
            }}
            onError={() => setFailed(true)}
            className={cn("pointer-events-none absolute left-0 top-0 max-w-none origin-top-left", !natural && "invisible")}
            style={{
              transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})`,
              imageRendering: view.scale >= 2 ? "pixelated" : "auto",
            }}
          />
        ) : null}
        {overlay && (
          <div
            aria-hidden
            className="pointer-events-none absolute"
            style={{
              left: overlay.left,
              top: overlay.top,
              width: overlay.width,
              height: overlay.height,
              backgroundImage:
                "linear-gradient(to right, rgb(127 127 127 / 0.55) 1px, transparent 1px), linear-gradient(to bottom, rgb(127 127 127 / 0.55) 1px, transparent 1px)",
              backgroundSize: `${view.scale}px ${view.scale}px`,
              backgroundPosition: `${overlay.offsetX}px ${overlay.offsetY}px`,
            }}
          />
        )}
      </div>
      <div className="flex shrink-0 items-center gap-2 border-t border-[var(--color-border)] px-3 py-1 text-[11px] text-[var(--color-fg-subtle)] tabular">
        {natural && (
          <span>
            {natural.w} × {natural.h}
          </span>
        )}
        <span>{file.extension ? file.extension.toUpperCase() : file.mimeType}</span>
        {blob.size > 0 && blob.type.startsWith("image/") && <span>{formatBytes(blob.size)}</span>}
        <span className="ml-auto">{Math.round(view.scale * 100)}%</span>
      </div>
    </div>
  );
}
