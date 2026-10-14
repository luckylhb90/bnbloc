import { useEffect, useRef, useState } from "react";
import { PauseIcon, PlayIcon } from "@phosphor-icons/react";
import { clock } from "../mediaTime";
import type { CompareViewProps } from "./registry";

// Two versions of a video or audio file in two players kept in step
// (docs/prd/2026-10-14-select-edit-review.md rule 7): one play/pause and one
// position for both, the newer one following the older when they drift.

function useObjectUrl(blob: Blob): string {
  const [url, setUrl] = useState("");
  useEffect(() => {
    const u = URL.createObjectURL(blob);
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [blob]);
  return url;
}

// Past this the second player is put back in step.
const DRIFT_S = 0.25;

export function MediaCompare({ prev, curr }: CompareViewProps) {
  const a = useObjectUrl(prev.blob);
  const b = useObjectUrl(curr.blob);
  const audio = curr.file.mimeType.startsWith("audio/");
  const one = useRef<HTMLVideoElement & HTMLAudioElement>(null);
  const two = useRef<HTMLVideoElement & HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const [time, setTime] = useState(0);
  const [length, setLength] = useState(0);

  useEffect(() => {
    const p = one.current;
    const q = two.current;
    if (!p || !q) return;
    const tick = () => {
      setTime(p.currentTime);
      if (Math.abs(q.currentTime - p.currentTime) > DRIFT_S && p.currentTime <= (q.duration || Infinity)) q.currentTime = p.currentTime;
    };
    const meta = () => setLength(Math.max(p.duration || 0, q.duration || 0));
    p.addEventListener("timeupdate", tick);
    p.addEventListener("loadedmetadata", meta);
    q.addEventListener("loadedmetadata", meta);
    const ended = () => setPlaying(false);
    p.addEventListener("ended", ended);
    return () => {
      p.removeEventListener("timeupdate", tick);
      p.removeEventListener("loadedmetadata", meta);
      q.removeEventListener("loadedmetadata", meta);
      p.removeEventListener("ended", ended);
    };
  }, [a, b]);

  const toggle = () => {
    const both = [one.current, two.current];
    if (playing) both.forEach((m) => m?.pause());
    else both.forEach((m) => void m?.play().catch(() => {}));
    setPlaying(!playing);
  };
  const seek = (t: number) => {
    for (const m of [one.current, two.current]) if (m) m.currentTime = t;
    setTime(t);
  };
  const Player = audio ? "audio" : "video";
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className={audio ? "flex flex-col gap-3 p-4" : "grid min-h-0 flex-1 grid-cols-2 gap-2 p-2"}>
        {[
          { ref: one, url: a, label: "Before" },
          { ref: two, url: b, label: "After" },
        ].map((s) => (
          <figure key={s.label} className="flex min-h-0 flex-col items-center gap-1">
            <figcaption className="text-[11px] text-[var(--color-fg-subtle)]">{s.label}</figcaption>
            {s.url && <Player ref={s.ref} src={s.url} playsInline preload="metadata" className={audio ? "w-full" : "max-h-full max-w-full rounded-[var(--radius)]"} />}
          </figure>
        ))}
      </div>
      <div className="flex shrink-0 items-center gap-2 border-t border-[var(--color-border)] px-3 py-1.5 text-[11px] text-[var(--color-fg-subtle)] tabular">
        <button type="button" onClick={toggle} className="tactile grid size-7 place-items-center rounded-full text-[var(--color-fg)] hover:bg-[var(--color-surface-2)]" aria-label={playing ? "Pause both" : "Play both"}>
          {playing ? <PauseIcon size={14} weight="fill" /> : <PlayIcon size={14} weight="fill" />}
        </button>
        <input type="range" min={0} max={length || 0} step={0.1} value={time} onChange={(e) => seek(Number(e.target.value))} aria-label="Position in both" className="min-w-0 flex-1" />
        <span>
          {clock(time)} / {clock(length)}
        </span>
      </div>
    </div>
  );
}
