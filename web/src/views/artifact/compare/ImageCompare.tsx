import { useEffect, useState } from "react";
import { cn } from "@/lib/cn";
import type { CompareViewProps } from "./registry";

// Two versions of an image: side by side, or one over the other with a
// slider revealing the newer. The bytes render as <img> from blob URLs, as
// the image viewer does (an SVG in <img> runs no script).

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
  const [mode, setMode] = useState<"side" | "slider">("side");
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
      </div>
      {mode === "side" ? (
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
