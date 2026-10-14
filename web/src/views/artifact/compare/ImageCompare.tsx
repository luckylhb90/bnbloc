import { useEffect, useRef, useState } from "react";
import { cn } from "@/lib/cn";
import { diffPixels } from "./imageDiff";
import type { CompareViewProps } from "./registry";

// Two versions of an image: side by side, one over the other with a slider
// revealing the newer, or the Difference: the pixels that changed, marked
// over the newer image (same size only). The bytes render as <img> from
// blob URLs, as the image viewer does (an SVG in <img> runs no script).

function useObjectUrl(blob: Blob): string {
  const [url, setUrl] = useState("");
  useEffect(() => {
    const u = URL.createObjectURL(blob);
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [blob]);
  return url;
}

export function ImageCompare({ prev, curr }: CompareViewProps) {
  const a = useObjectUrl(prev.blob);
  const b = useObjectUrl(curr.blob);
  const [mode, setMode] = useState<"side" | "slider" | "diff">("side");
  const [at, setAt] = useState(50);
  const tab = (m: typeof mode, label: string) => (
    <button
      type="button"
      aria-pressed={mode === m}
      onClick={() => setMode(m)}
      className={cn("tactile rounded-[var(--radius)] px-2 py-1 text-xs", mode === m ? "bg-[var(--color-elevated)] text-[var(--color-fg)]" : "text-[var(--color-fg-muted)]")}
    >
      {label}
    </button>
  );
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center gap-1 border-b border-[var(--color-border)] px-2 py-1" role="group" aria-label="Comparison layout">
        {tab("side", "Side by side")}
        {tab("slider", "Slider")}
        {tab("diff", "Difference")}
      </div>
      {mode === "diff" ? (
        <Difference a={a} b={b} />
      ) : mode === "side" ? (
        <div className="grid min-h-0 flex-1 grid-cols-2 gap-2 overflow-auto p-2">
          {[
            { url: a, label: "Before" },
            { url: b, label: "After" },
          ].map((s) => (
            <figure key={s.label} className="flex min-h-0 flex-col items-center gap-1">
              <figcaption className="text-[11px] text-[var(--color-fg-subtle)]">{s.label}</figcaption>
              {s.url && <img src={s.url} alt={s.label} className="max-h-full max-w-full object-contain" />}
            </figure>
          ))}
        </div>
      ) : (
        <div className="relative min-h-0 flex-1 overflow-hidden p-2">
          <div className="relative mx-auto h-full w-fit">
            {a && <img src={a} alt="Before" className="block max-h-full max-w-full object-contain" />}
            {b && (
              <img
                src={b}
                alt="After"
                className="absolute inset-0 h-full w-full object-contain"
                style={{ clipPath: `inset(0 0 0 ${at}%)` }}
              />
            )}
            <div className="pointer-events-none absolute inset-y-0 w-px bg-[var(--color-accent)]" style={{ left: `${at}%` }} />
          </div>
          <input
            type="range"
            min={0}
            max={100}
            value={at}
            onChange={(e) => setAt(Number(e.target.value))}
            aria-label="Reveal the newer version"
            className="absolute inset-x-6 bottom-3"
          />
        </div>
      )}
    </div>
  );
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("Could not decode the image"));
    img.src = url;
  });
}

function pixels(img: HTMLImageElement): ImageData {
  const c = document.createElement("canvas");
  c.width = img.naturalWidth;
  c.height = img.naturalHeight;
  const ctx = c.getContext("2d");
  if (!ctx) throw new Error("No canvas");
  ctx.drawImage(img, 0, 0);
  return ctx.getImageData(0, 0, c.width, c.height);
}

/** The changed pixels, marked over the newer image. */
function Difference({ a, b }: { readonly a: string; readonly b: string }) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [note, setNote] = useState("Comparing pixels…");
  useEffect(() => {
    if (!a || !b) return;
    let live = true;
    void Promise.all([loadImage(a), loadImage(b)]).then(
      ([before, after]) => {
        if (!live) return;
        if (before.naturalWidth !== after.naturalWidth || before.naturalHeight !== after.naturalHeight) {
          setNote(`The sizes differ (${before.naturalWidth}×${before.naturalHeight} → ${after.naturalWidth}×${after.naturalHeight}): compare them side by side.`);
          return;
        }
        const pa = pixels(before);
        const pb = pixels(after);
        const { out, changed } = diffPixels(pa.data, pb.data);
        const canvas = canvasRef.current;
        if (!canvas) return;
        canvas.width = pb.width;
        canvas.height = pb.height;
        const ctx = canvas.getContext("2d");
        if (ctx) {
          const img = ctx.createImageData(pb.width, pb.height);
          img.data.set(out);
          ctx.putImageData(img, 0, 0);
        }
        const share = (changed / (pb.width * pb.height)) * 100;
        setNote(changed === 0 ? "No pixel changed." : `${share < 0.1 ? "<0.1" : share.toFixed(1)}% of the pixels changed (marked).`);
      },
      () => live && setNote("These images could not be compared pixel by pixel."),
    );
    return () => {
      live = false;
    };
  }, [a, b]);
  return (
    <div className="flex min-h-0 flex-1 flex-col items-center gap-2 overflow-auto p-2">
      <p className="text-[11px] text-[var(--color-fg-subtle)]" role="status">
        {note}
      </p>
      <canvas ref={canvasRef} aria-label="Changed pixels" className="max-h-full max-w-full object-contain" />
    </div>
  );
}
