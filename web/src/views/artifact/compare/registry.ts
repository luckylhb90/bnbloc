import type React from "react";
import type { CompareUnit } from "../../../../previewer-sdk/src/compare.ts";
import { createFileRegistry, useRegistryRevision, type FileClaimant } from "../../../lib/fileRegistry.ts";
import type { ArtifactFileInfo } from "../registry.ts";

// The version comparers (docs/prd/2026-10-14-file-prompts-and-compare.md
// rule 6): a registry of strategies, one per kind of file, built-in
// (compare/builtin.ts) or from an installed previewer that reads its format
// into units (compare/remote.ts). The Compare button shows whenever one
// claims the file; selection is the generic file registry's.

/** One version, as a comparer gets it. */
export interface CompareInput {
  readonly blob: Blob;
  readonly file: ArtifactFileInfo;
}

/** A version split into units; truncated when a bound cut it. */
export interface UnitList {
  readonly units: readonly CompareUnit[];
  readonly truncated?: boolean;
}

export interface CompareViewProps {
  readonly prev: CompareInput;
  readonly curr: CompareInput;
}

/** How a comparer compares: by units (the shared engine and view), or with
 *  its own view (images). */
export type CompareStrategy =
  | {
      readonly kind: "units";
      /** What a unit is called, singular and plural ("slide", "slides");
       *  absent for an installed previewer's units (their labels say). */
      readonly noun?: readonly [string, string];
      extract(input: CompareInput, signal: AbortSignal): Promise<UnitList>;
    }
  | { readonly kind: "view"; readonly View: React.ComponentType<CompareViewProps> };

export interface VersionComparer extends FileClaimant {
  readonly label: string;
  /** The strategy, loaded on first use (code-split like viewers). */
  load(): Promise<CompareStrategy>;
}

const comparers = createFileRegistry<VersionComparer>();

export const registerComparer = comparers.register;
export const unregisterComparer = comparers.unregister;

/** The comparer for a file; null = its versions cannot be compared. */
export function selectComparer(file: ArtifactFileInfo): VersionComparer | null {
  return comparers.select(file);
}

/** Re-renders on registry changes (installed previewers come and go). */
export function useComparerRegistry(): number {
  return useRegistryRevision(comparers);
}
