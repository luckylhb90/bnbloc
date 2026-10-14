import { useEffect, useState } from "react";
import type { Artifact, ArtifactDetail } from "@/lib/wire.gen";
import { errorText } from "@/lib/errors";
import "@/views/artifact/compare/builtin";
import type { CompareInput, CompareStrategy, UnitList, VersionComparer } from "@/views/artifact/compare/registry";
import { fileInfoFor, loadVersionBlob, versionMeta } from "./artifactData";
import { UnitDiff } from "./VersionDiff";

// Compare two versions of a file with the comparer that claims it
// (docs/prd/2026-10-14-file-prompts-and-compare.md rule 6): load both
// versions, then either split each into units for the shared view, or hand
// both to the comparer's own view. A failure says so and leaves the tab
// usable.

type State =
  | { readonly phase: "loading" }
  | { readonly phase: "error"; readonly message: string }
  | { readonly phase: "units"; readonly noun?: readonly [string, string]; readonly prev: UnitList; readonly curr: UnitList }
  | { readonly phase: "view"; readonly strategy: Extract<CompareStrategy, { kind: "view" }>; readonly prev: CompareInput; readonly curr: CompareInput };

export function CompareView({
  roomId,
  artifact,
  detail,
  comparer,
  prevVersion,
  currVersion,
}: {
  readonly roomId: string;
  readonly artifact: Artifact;
  readonly detail: ArtifactDetail;
  readonly comparer: VersionComparer;
  readonly prevVersion: number;
  readonly currVersion: number;
}) {
  const [state, setState] = useState<State>({ phase: "loading" });
  const key = `${artifact.artifactId}:${prevVersion}:${currVersion}:${comparer.id}`;
  useEffect(() => {
    const abort = new AbortController();
    setState({ phase: "loading" });
    const input = async (v: number): Promise<CompareInput> => {
      const meta = versionMeta(detail, v);
      return { blob: await loadVersionBlob(roomId, artifact, v, meta), file: fileInfoFor(artifact, v, meta) };
    };
    void (async () => {
      const [strategy, prev, curr] = await Promise.all([comparer.load(), input(prevVersion), input(currVersion)]);
      if (strategy.kind === "view") return { phase: "view", strategy, prev, curr } as const;
      const [a, b] = await Promise.all([strategy.extract(prev, abort.signal), strategy.extract(curr, abort.signal)]);
      return { phase: "units", noun: strategy.noun, prev: a, curr: b } as const;
    })().then(
      (next) => !abort.signal.aborted && setState(next),
      (e: unknown) => !abort.signal.aborted && setState({ phase: "error", message: errorText(e, "Could not compare these versions") }),
    );
    return () => abort.abort();
    // key names the inputs; detail and artifact objects refetch without changing them.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  if (state.phase === "loading") return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Comparing versions…</p>;
  if (state.phase === "error") return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">{state.message}</p>;
  if (state.phase === "view") {
    const View = state.strategy.View;
    return <View prev={state.prev} curr={state.curr} />;
  }
  return <UnitDiff prev={state.prev.units} curr={state.curr.units} noun={state.noun} truncated={state.prev.truncated || state.curr.truncated} />;
}
