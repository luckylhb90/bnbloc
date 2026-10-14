import type { Artifact, ArtifactDetail } from "@/lib/wire.gen";
import { useVersionChanges } from "./useVersionChanges";
import { UnitDiff } from "./VersionDiff";

// Compare two versions of a file with the comparer that claims it
// (docs/prd/2026-10-14-file-prompts-and-compare.md rule 6), through the one
// changes computation (useVersionChanges). A failure says so and leaves the
// tab usable.

export function CompareView({
  roomId,
  artifact,
  detail,
  prevVersion,
  currVersion,
  onShow,
}: {
  readonly roomId: string;
  readonly artifact: Artifact;
  readonly detail: ArtifactDetail;
  readonly prevVersion: number;
  readonly currVersion: number;
  /** Shows a unit of the current version in the preview (its `at`). */
  readonly onShow?: (at: string) => void;
}) {
  const changes = useVersionChanges(roomId, artifact, detail, prevVersion, currVersion);
  if (changes.phase === "none") return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">These versions cannot be compared.</p>;
  if (changes.phase === "loading") return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Comparing versions…</p>;
  if (changes.phase === "error") return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">{changes.message}</p>;
  if (changes.phase === "view") {
    const View = changes.strategy.View;
    return <View prev={changes.prev} curr={changes.curr} />;
  }
  return <UnitDiff changes={changes.changes} noun={changes.noun} truncated={changes.prev.truncated || changes.curr.truncated} onShow={onShow} />;
}
