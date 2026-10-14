import { useSyncExternalStore } from "react";
import { matchSpecificity, type FileFacts, type MatchSpec } from "./fileMatch.ts";

// A runtime registry of things that claim files (Registry + Strategy): the
// artifact viewers, the version comparers. Built-ins register at import,
// installed previewer components at runtime from the member catalog, so
// selection, "Open with…" and Compare treat both the same way
// (docs/prd/2026-10-14-file-prompts-and-compare.md rules 1 and 6).
//
// Selection (candidates): every entry whose match claims the file, ordered by
//   1. specificity (fileMatch.matchSpecificity);
//   2. rank, ascending (built-ins sit at 10, so a component that should beat
//      a built-in of the same specificity declares < 10);
//   3. an installed component before a built-in, then id.
// An entry with maxBytes drops out for larger files. select picks the
// member's explicit choice while it is still a candidate, else the best.

export interface FileClaimant {
  readonly id: string;
  readonly match: MatchSpec;
  /** Lower wins among equally specific matches; built-ins use 10. */
  readonly rank: number;
  readonly source: "builtin" | "component";
  /** Files larger than this fall through to the next candidate. */
  readonly maxBytes?: number;
}

export interface Candidate<T> {
  readonly item: T;
  readonly specificity: number;
}

export interface FileRegistry<T extends FileClaimant> {
  /** Registers (or replaces, by id) an entry; returns its unregister. */
  register(item: T): () => void;
  unregister(id: string): void;
  list(): readonly T[];
  subscribe(listener: () => void): () => void;
  revision(): number;
  candidates(file: FileFacts & { readonly sizeBytes?: number }, from?: readonly T[]): Candidate<T>[];
  select(file: FileFacts & { readonly sizeBytes?: number }, preferredId?: string, from?: readonly T[]): T | null;
}

export function createFileRegistry<T extends FileClaimant>(): FileRegistry<T> {
  const items: T[] = [];
  const listeners = new Set<() => void>();
  let revision = 0;
  const changed = () => {
    revision += 1;
    for (const listener of listeners) listener();
  };
  const reg: FileRegistry<T> = {
    register(item) {
      const at = items.findIndex((x) => x.id === item.id);
      if (at >= 0) items[at] = item;
      else items.push(item);
      changed();
      return () => reg.unregister(item.id);
    },
    unregister(id) {
      const at = items.findIndex((x) => x.id === id);
      if (at < 0) return;
      items.splice(at, 1);
      changed();
    },
    list: () => [...items],
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    revision: () => revision,
    candidates(file, from = items) {
      const out: Candidate<T>[] = [];
      for (const item of from) {
        const specificity = matchSpecificity(item.match, file);
        if (specificity === 0) continue;
        if (item.maxBytes !== undefined && file.sizeBytes !== undefined && file.sizeBytes > item.maxBytes) continue;
        out.push({ item, specificity });
      }
      return out.sort(
        (a, b) =>
          b.specificity - a.specificity ||
          a.item.rank - b.item.rank ||
          (a.item.source === b.item.source ? 0 : a.item.source === "component" ? -1 : 1) ||
          a.item.id.localeCompare(b.item.id),
      );
    },
    select(file, preferredId, from = items) {
      const candidates = reg.candidates(file, from);
      if (preferredId) {
        const preferred = candidates.find((c) => c.item.id === preferredId);
        if (preferred) return preferred.item;
      }
      return candidates[0]?.item ?? null;
    },
  };
  return reg;
}

/** Re-renders on a registry's changes (runtime installs). */
export function useRegistryRevision(reg: Pick<FileRegistry<FileClaimant>, "subscribe" | "revision">): number {
  return useSyncExternalStore(reg.subscribe, reg.revision, reg.revision);
}
