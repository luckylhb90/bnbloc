// The files a paste carries (a file copied in the Finder, a screenshot).
// Pure, unit-tested under node.

/** The files of a paste: its file list, else its file items (some engines
 *  fill only one of them). */
export function pastedFiles(data: Pick<DataTransfer, "files" | "items"> | null): File[] {
  if (!data) return [];
  const listed = Array.from(data.files ?? []);
  if (listed.length > 0) return listed;
  const out: File[] = [];
  for (const item of Array.from(data.items ?? [])) {
    if (item.kind !== "file") continue;
    const f = item.getAsFile();
    if (f) out.push(f);
  }
  return out;
}
