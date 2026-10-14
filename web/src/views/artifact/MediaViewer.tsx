import { useEffect, useRef, useState } from "react";
import { XIcon } from "@phosphor-icons/react";
import { clock, rangeSelection } from "./mediaTime";
import type { ArtifactViewerProps } from "./registry";

// MediaViewer: the platform's own audio/video player on a blob URL (the
// bytes were fetched with the session; nothing streams from a credentialed
// URL). Formats the webview cannot decode report it instead of a dead player.
// "Mark start" and "Mark end" select a time range the Ask AI panel names (a
// time selection; docs/prd/2026-10-14-select-edit-review.md rule 7).

export default function MediaViewer({ blob, file, onSelection }: ArtifactViewerProps) {
  const [url, setUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  const mediaRef = useRef<HTMLVideoElement & HTMLAudioElement>(null);
  const barRef = useRef<HTMLDivElement | null>(null);
  const [start, setStart] = useState<number | null>(null);
  const [end, setEnd] = useState<number | null>(null);
  const onSelectionRef = useRef(onSelection);
  onSelectionRef.current = onSelection;
  useEffect(() => {
    const next = URL.createObjectURL(blob);
    setUrl(next);
    setFailed(false);
    return () => URL.revokeObjectURL(next);
  }, [blob]);

  // A marked range is the tab's selection, its action beside the range bar.
  const hadRange = useRef(false);
  useEffect(() => {
    const media = mediaRef.current;
    const bar = barRef.current;
    if (start !== null && end !== null && end !== start && media && bar) {
      hadRange.current = true;
      const box = bar.offsetParent?.getBoundingClientRect();
      const r = bar.getBoundingClientRect();
      onSelectionRef.current?.({
        ...rangeSelection(start, end, media.duration || Math.max(start, end)),
        kind: "time",
        ...(box ? { rect: { x: r.left - box.left, y: r.top - box.top, w: r.width, h: r.height } } : {}),
      });
    } else if (hadRange.current) {
      hadRange.current = false;
      onSelectionRef.current?.(null);
    }
  }, [start, end]);

  const mark = (which: "start" | "end") => {
    const t = mediaRef.current?.currentTime ?? 0;
    if (which === "start") setStart(t);
    else setEnd(t);
  };
  const clear = () => {
    setStart(null);
    setEnd(null);
  };
  const playRange = () => {
    const media = mediaRef.current;
    if (!media || start === null || end === null) return;
    const [a, b] = [Math.min(start, end), Math.max(start, end)];
    media.currentTime = a;
    void media.play();
    const stop = () => {
      if (media.currentTime >= b) {
        media.pause();
        media.removeEventListener("timeupdate", stop);
      }
    };
    media.addEventListener("timeupdate", stop);
  };

  if (!url) return null;
  const audio = file.mimeType.startsWith("audio/");
  const btn = "tactile rounded-[var(--radius)] border border-[var(--color-border)] px-2 py-1 text-xs text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] disabled:opacity-50";
  return (
    <div className="relative flex h-full flex-col bg-[var(--color-bg)]">
      <div className="grid min-h-0 flex-1 place-items-center p-4">
        {failed ? (
          <p className="text-sm text-[var(--color-fg-subtle)]">This format cannot be played here. Download it to open it locally.</p>
        ) : audio ? (
          <audio ref={mediaRef} controls src={url} onError={() => setFailed(true)} className="w-full max-w-xl" aria-label={file.name} />
        ) : (
          <video ref={mediaRef} controls playsInline src={url} onError={() => setFailed(true)} className="max-h-full max-w-full rounded-[var(--radius)]" aria-label={file.name} />
        )}
      </div>
      {!failed && (
        <div ref={barRef} className="flex shrink-0 flex-wrap items-center gap-1.5 border-t border-[var(--color-border)] px-3 py-1.5" role="group" aria-label="Select a time range">
          <button type="button" className={btn} onClick={() => mark("start")}>
            Mark start{start !== null ? ` · ${clock(start)}` : ""}
          </button>
          <button type="button" className={btn} onClick={() => mark("end")}>
            Mark end{end !== null ? ` · ${clock(end)}` : ""}
          </button>
          {start !== null && end !== null && end !== start && (
            <>
              <button type="button" className={btn} onClick={playRange}>
                Play {clock(Math.min(start, end))}–{clock(Math.max(start, end))}
              </button>
              <button type="button" className={btn} onClick={clear} aria-label="Clear the time range" title="Clear the time range">
                <XIcon size={12} />
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
