import { useMemo, useState } from "react";
import { changeCounts, diffUnits, type UnitChange } from "@/lib/compareUnits";
import { diffLines } from "@/lib/diff";
import { cn } from "@/lib/cn";

// The units view of Compare (docs/prd/2026-10-14-file-prompts-and-compare.md
// rule 8): what changed between two versions, unit by unit (a slide, a
// cell, a paragraph), each changed unit with a line diff of its text, the
// unchanged ones collapsed. A whole text is one unlabeled unit: its line
// diff alone.

/** A line diff of two texts, inline. */
export function LineDiff({ prev, curr }: { readonly prev: string; readonly curr: string }) {
  // One side empty (an added or removed unit): its lines, not a diff
  // against an empty line.
  const rows = useMemo(
    () =>
      prev === ""
        ? curr.split("\n").map((text) => ({ type: "add" as const, text }))
        : curr === ""
          ? prev.split("\n").map((text) => ({ type: "del" as const, text }))
          : diffLines(prev, curr),
    [prev, curr],
  );
  return (
    <div className="font-mono text-[12px] leading-5">
      {rows.map((r, i) => (
        <div
          key={i}
          className={cn(
            "flex gap-2 px-3",
            r.type === "add" && "bg-[color-mix(in_srgb,var(--color-accent)_12%,transparent)]",
            r.type === "del" && "bg-[color-mix(in_srgb,var(--color-danger,#b91c1c)_12%,transparent)] opacity-70",
          )}
        >
          <span className="w-4 shrink-0 select-none text-[var(--color-fg-subtle)]">{r.type === "add" ? "+" : r.type === "del" ? "−" : " "}</span>
          <span className="whitespace-pre-wrap break-all">{r.text || " "}</span>
        </div>
      ))}
    </div>
  );
}

const BADGE: Record<Exclude<UnitChange["kind"], "same">, string> = { changed: "Changed", added: "Added", removed: "Removed" };

function plural(n: number, noun: readonly [string, string]): string {
  return `${n} ${n === 1 ? noun[0] : noun[1]}`;
}

export function UnitDiff({
  prev,
  curr,
  noun,
  truncated,
}: {
  readonly prev: Parameters<typeof diffUnits>[0];
  readonly curr: Parameters<typeof diffUnits>[1];
  readonly noun: readonly [string, string];
  readonly truncated?: boolean;
}) {
  const changes = useMemo(() => diffUnits(prev, curr), [prev, curr]);
  const [showSame, setShowSame] = useState(false);
  const counts = changeCounts(changes);
  if (counts.changed + counts.added + counts.removed === 0) {
    return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">No changes.</p>;
  }
  // A whole text: its line diff, nothing around it.
  if (changes.length === 1 && changes[0].kind === "changed" && !changes[0].curr.label) {
    return (
      <div className="h-full overflow-auto py-2">
        <LineDiff prev={changes[0].prev.text} curr={changes[0].curr.text} />
      </div>
    );
  }
  const summary = [
    counts.changed && `${plural(counts.changed, noun)} changed`,
    counts.added && `${counts.added} added`,
    counts.removed && `${counts.removed} removed`,
  ].filter(Boolean);
  return (
    <div className="h-full overflow-auto">
      <div className="sticky top-0 z-10 flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-xs text-[var(--color-fg-muted)]">
        <span className="min-w-0 flex-1 truncate">
          {summary.join(", ")}
          {truncated && " · showing the first part of a large file"}
        </span>
        {counts.same > 0 && (
          <button type="button" onClick={() => setShowSame((v) => !v)} className="tactile shrink-0 text-[var(--color-accent)]">
            {showSame ? "Hide" : "Show"} {plural(counts.same, noun)} unchanged
          </button>
        )}
      </div>
      <ul className="flex flex-col gap-2 p-2">
        {changes.map((c, i) => {
          if (c.kind === "same" && !showSame) return null;
          const label = (c.kind === "removed" ? c.prev.label : c.curr.label) || `${noun[0]} ${i + 1}`;
          return (
            <li key={i} className={cn("overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)]", c.kind === "same" && "opacity-60")}>
              <div className="flex items-center gap-2 border-b border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-1 text-xs">
                <span className="min-w-0 flex-1 truncate font-medium text-[var(--color-fg)]">{label}</span>
                {c.kind !== "same" && <span className="shrink-0 text-[10px] uppercase tracking-wide text-[var(--color-fg-subtle)]">{BADGE[c.kind]}</span>}
              </div>
              <div className="py-1">
                {c.kind === "changed" ? (
                  <LineDiff prev={c.prev.text} curr={c.curr.text} />
                ) : c.kind === "added" ? (
                  <LineDiff prev="" curr={c.curr.text} />
                ) : c.kind === "removed" ? (
                  <LineDiff prev={c.prev.text} curr="" />
                ) : (
                  <p className="whitespace-pre-wrap px-3 font-mono text-[12px] leading-5 text-[var(--color-fg-muted)]">{c.curr.text}</p>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
