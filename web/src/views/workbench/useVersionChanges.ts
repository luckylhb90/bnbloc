import { useEffect, useState } from "react";
import { diffUnits, type UnitChange } from "@/lib/compareUnits";
import { errorText } from "@/lib/errors";
import type { Artifact, ArtifactDetail } from "@/lib/wire.gen";
import "@/views/artifact/compare/builtin";
import { selectComparer, useComparerRegistry, type CompareInput, type CompareStrategy, type UnitList } from "@/views/artifact/compare/registry";
import { versionUnits } from "@/views/artifact/compare/versionUnits";
import { fileInfoFor, loadVersionBlob, versionMeta } from "./artifactData";

// What changed between two versions of a file: the one computation behind
// Compare and the change cards (docs/prd/2026-10-14-select-edit-review.md
// rule 4). The comparer that claims the file either splits both versions
// into units (cached, versionUnits) and the changes are diffed, or renders
// its own view of the two inputs.

export type VersionChanges =
  | { readonly phase: "none" }
  | { readonly phase: "loading" }
  | { readonly phase: "error"; readonly message: string }
  | {
      readonly phase: "units";
      readonly noun?: readonly [string, string];
      readonly prev: UnitList;
      readonly curr: UnitList;
      readonly changes: readonly UnitChange[];
    }
  | {
      readonly phase: "view";
      readonly strategy: Extract<CompareStrategy, { kind: "view" }>;
      readonly prev: CompareInput;
      readonly curr: CompareInput;
    };

export function useVersionChanges(
  roomId: string,
  artifact: Artifact | null,
  detail: ArtifactDetail | null,
  prevVersion: number | null,
  currVersion: number | null,
  enabled = true,
): VersionChanges {
  useComparerRegistry();
  const file = artifact && currVersion ? fileInfoFor(artifact, currVersion, versionMeta(detail, currVersion)) : null;
  const comparer = file ? selectComparer(file) : null;
  const key = artifact && comparer && prevVersion && currVersion ? `${artifact.artifactId}:${prevVersion}:${currVersion}:${comparer.id}` : "";
  const [state, setState] = useState<{ key: string; value: VersionChanges }>({ key: "", value: { phase: "loading" } });
  useEffect(() => {
    if (!enabled || !key || !artifact || !comparer || !prevVersion || !currVersion) return;
    let live = true;
    const input = (v: number) => async (): Promise<CompareInput> => {
      const meta = versionMeta(detail, v);
      return { blob: await loadVersionBlob(roomId, artifact, v, meta), file: fileInfoFor(artifact, v, meta) };
    };
    void (async (): Promise<VersionChanges> => {
      const strategy = await comparer.load();
      if (strategy.kind === "view") {
        const [prev, curr] = await Promise.all([input(prevVersion)(), input(currVersion)()]);
        return { phase: "view", strategy, prev, curr };
      }
      const unitKey = (v: number) => `${artifact.artifactId}:${v}:${comparer.id}`;
      const [prev, curr] = await Promise.all([
        versionUnits(unitKey(prevVersion), strategy, input(prevVersion)),
        versionUnits(unitKey(currVersion), strategy, input(currVersion)),
      ]);
      return { phase: "units", noun: strategy.noun, prev, curr, changes: diffUnits(prev.units, curr.units) };
    })().then(
      (value) => live && setState({ key, value }),
      (e: unknown) => live && setState({ key, value: { phase: "error", message: errorText(e, "Could not compare these versions") } }),
    );
    return () => {
      live = false;
    };
    // key names the inputs; detail and artifact objects refetch without changing them.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, enabled]);
  if (!comparer || !prevVersion || !currVersion) return { phase: "none" };
  return state.key === key ? state.value : { phase: "loading" };
}
