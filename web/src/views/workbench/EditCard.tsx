import { useEffect, useRef, useState } from "react";
import { ArrowCounterClockwiseIcon, CrosshairSimpleIcon, GitDiffIcon, WarningIcon } from "@phosphor-icons/react";
import { changeCounts, type UnitChange } from "../../../previewer-sdk/src/compare.ts";
import { outsideSelection } from "@/lib/editScope";
import { errorText } from "@/lib/errors";
import type { Artifact, ArtifactDetail, ArtifactVersion, FileSelection } from "@/lib/wire.gen";
import { useToast } from "@/ui/Toast";
import { restoreVersion, versionMeta } from "./artifactData";
import { useVersionChanges } from "./useVersionChanges";

// The change card (docs/prd/2026-10-14-select-edit-review.md rules 4–5): what
// one version a request produced changed (against the version it was
// published on), whether it stayed in the request's selection, and the ways
// on: View changes (Compare), Show (the preview at the first change), Undo
// (the earlier version restored as the next one; never a rewind).

const LISTED = 4;

function unitLabel(c: UnitChange): string {
  return (c.kind === "removed" ? c.prev.label : c.curr.label) || "Part";
}

const KIND: Record<Exclude<UnitChange["kind"], "same">, string> = { changed: "Changed", added: "Added", removed: "Removed" };

/** True once the element has been on screen (its changes load then). */
function useSeen<T extends Element>(): [React.RefObject<T | null>, boolean] {
  const ref = useRef<T | null>(null);
  const [seen, setSeen] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el || seen) return;
    const io = new IntersectionObserver(([e]) => e?.isIntersecting && setSeen(true));
    io.observe(el);
    return () => io.disconnect();
  }, [seen]);
  return [ref, seen];
}

export function EditCard({
  roomId,
  artifact,
  detail,
  version,
  selection,
  onViewChanges,
  onShow,
}: {
  readonly roomId: string;
  readonly artifact: Artifact;
  readonly detail: ArtifactDetail;
  readonly version: ArtifactVersion;
  /** The request's selection (rule 5). */
  readonly selection?: FileSelection;
  readonly onViewChanges: (prev: number, curr: number) => void;
  readonly onShow?: (version: number, at: string) => void;
}) {
  const toast = useToast();
  const [ref, seen] = useSeen<HTMLDivElement>();
  const prev = version.parentVersion;
  const changes = useVersionChanges(roomId, artifact, detail, prev > 0 ? prev : null, version.version, seen);
  const [undone, setUndone] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const current = artifact.currentVersion === version.version;

  const undo = async () => {
    setBusy(true);
    try {
      const summary = current ? `Undid v${version.version}` : `Restored v${prev}`;
      const next = await restoreVersion(roomId, artifact, prev, versionMeta(detail, prev), summary);
      setUndone(next);
      toast.success(`${summary} as v${next}`);
    } catch (e) {
      toast.error(errorText(e, "Could not restore the earlier version"));
    } finally {
      setBusy(false);
    }
  };

  const units = changes.phase === "units" ? changes.changes.filter((c): c is Exclude<UnitChange, { kind: "same" }> => c.kind !== "same") : [];
  const counts = changes.phase === "units" ? changeCounts(changes.changes) : null;
  const outside = changes.phase === "units" && selection ? outsideSelection(changes.changes, selection) : [];
  const firstAt = units.find((c) => c.kind !== "removed" && c.curr.at);
  const btn = "tactile inline-flex items-center gap-1 rounded-[var(--radius)] px-2 py-1 text-[11px] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] disabled:opacity-50";

  return (
    <div ref={ref} data-edit-card={version.version} className="my-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-2 text-xs">
      <div className="flex items-center gap-2">
        <span className="font-medium text-[var(--color-fg)]">v{version.version}</span>
        <span className="min-w-0 flex-1 truncate text-[var(--color-fg-subtle)]">{version.summary || `from v${prev}`}</span>
      </div>
      <div className="mt-1 text-[var(--color-fg-muted)]">
        {prev <= 0 ? (
          <p>The first version.</p>
        ) : changes.phase === "loading" ? (
          <p>Reading the changes…</p>
        ) : changes.phase === "units" && counts ? (
          counts.changed + counts.added + counts.removed === 0 ? (
            <p>No visible changes.</p>
          ) : (
            <ul className="flex flex-col gap-0.5">
              {units.slice(0, LISTED).map((c, i) => (
                <li key={i} className="truncate">
                  <span className="text-[var(--color-fg-subtle)]">{KIND[c.kind]} · </span>
                  {unitLabel(c)}
                </li>
              ))}
              {units.length > LISTED && <li className="text-[var(--color-fg-subtle)]">and {units.length - LISTED} more</li>}
            </ul>
          )
        ) : (
          <p>A new version{changes.phase === "error" ? ` (${changes.message})` : ""}.</p>
        )}
        {outside.length > 0 && (
          <p className="mt-1 flex items-start gap-1 text-[var(--color-warning,#b45309)]">
            <WarningIcon size={13} className="mt-px shrink-0" aria-hidden="true" />
            <span>
              Also changed outside your selection: {outside.slice(0, 3).map(unitLabel).join(", ")}
              {outside.length > 3 ? ` and ${outside.length - 3} more` : ""}
            </span>
          </p>
        )}
      </div>
      {prev > 0 && (
        <div className="mt-1.5 flex flex-wrap items-center gap-1">
          <button type="button" className={btn} onClick={() => onViewChanges(prev, version.version)}>
            <GitDiffIcon size={13} /> View changes
          </button>
          {onShow && firstAt && firstAt.kind !== "removed" && firstAt.curr.at && (
            <button type="button" className={btn} onClick={() => firstAt.curr.at && onShow(version.version, firstAt.curr.at)}>
              <CrosshairSimpleIcon size={13} /> Show
            </button>
          )}
          {undone ? (
            <span className="px-2 text-[11px] text-[var(--color-fg-subtle)]">Restored as v{undone}</span>
          ) : (
            <button type="button" className={btn} disabled={busy} onClick={() => void undo()} title={current ? `Publish v${prev} again as the next version` : `Publish v${prev} again on top of v${artifact.currentVersion}`}>
              <ArrowCounterClockwiseIcon size={13} /> {current ? "Undo" : `Restore v${prev}`}
            </button>
          )}
        </div>
      )}
    </div>
  );
}
