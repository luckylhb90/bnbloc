import type { CompareInput, CompareStrategy, UnitList } from "./registry";

// Units of a version, read once (docs/prd/2026-10-14-select-edit-review.md
// rule 4): Compare and the change cards ask for the same versions, and an
// Office file or a PDF is read in a hidden frame, so a version's units are
// cached by artifact, version and comparer (the newest 24). A read that
// fails is not kept: the next ask tries again. A read runs to its end even
// when one asker leaves: another may be waiting on it.

const MAX = 24;
const cache = new Map<string, Promise<UnitList>>();

export function versionUnits(
  key: string,
  strategy: Extract<CompareStrategy, { kind: "units" }>,
  input: () => Promise<CompareInput>,
): Promise<UnitList> {
  const hit = cache.get(key);
  if (hit) {
    cache.delete(key);
    cache.set(key, hit);
    return hit;
  }
  const pending = input().then((i) => strategy.extract(i, new AbortController().signal));
  cache.set(key, pending);
  pending.catch(() => {
    if (cache.get(key) === pending) cache.delete(key);
  });
  while (cache.size > MAX) cache.delete(cache.keys().next().value as string);
  return pending;
}
