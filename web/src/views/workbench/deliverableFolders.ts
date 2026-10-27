// Folders of the room's deliverables (docs/prd/2026-10-27-repo-files-and-folders.md):
// a deliverable's title is its workspace path ("reports/q3/summary.md"), so
// its folders are the path's directories. A deliverable without one (a
// document a member created) sits at the top.

export interface FolderEntry {
  readonly name: string;
  /** The folder's path from the top, "a/b" (no leading or trailing slash). */
  readonly path: string;
  /** Deliverables anywhere beneath it. */
  readonly count: number;
}

/** The folder segments of a deliverable's path ([] at the top). */
export function folderSegments(title: string): string[] {
  const segs = title.split("/").filter((s) => s !== "" && s !== ".");
  return segs.slice(0, -1);
}

/**
 * What one folder holds: its subfolders (by name, with how many deliverables
 * each holds) and the deliverables directly in it, in the order given.
 */
export function folderContents<T extends { readonly title: string }>(items: readonly T[], folder: string): { folders: FolderEntry[]; files: T[] } {
  const at = folder === "" ? [] : folder.split("/");
  const folders = new Map<string, number>();
  const files: T[] = [];
  for (const item of items) {
    const segs = folderSegments(item.title);
    if (segs.length < at.length || at.some((s, i) => segs[i] !== s)) continue;
    if (segs.length === at.length) {
      files.push(item);
      continue;
    }
    const name = segs[at.length];
    folders.set(name, (folders.get(name) ?? 0) + 1);
  }
  return {
    folders: [...folders.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, count]) => ({ name, path: [...at, name].join("/"), count })),
    files,
  };
}

/** The breadcrumb of a folder: each ancestor's name and path, top first. */
export function folderCrumbs(folder: string): { name: string; path: string }[] {
  if (folder === "") return [];
  const segs = folder.split("/");
  return segs.map((name, i) => ({ name, path: segs.slice(0, i + 1).join("/") }));
}
