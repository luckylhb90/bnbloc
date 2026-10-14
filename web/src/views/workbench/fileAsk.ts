// The file's Ask AI thread (docs/prd/2026-10-14-ai-document-editing.md rule
// 1): not a chat of its own, but the room conversation's messages about one
// file and what answered them. Pure (no React), unit-tested under node.

import type { FeedItem } from "../room/types.ts";
import type { Selection } from "../../../previewer-sdk/src/protocol.ts";
import type { ArtifactVersion, FileSelection } from "../../lib/wire.gen.ts";

/** The requests that named artifactId (payload.fileRef), in feed order,
 *  each followed by the agent rows and notices of its job (sourceSeq). */
export function fileThread(feed: readonly FeedItem[], artifactId: string): FeedItem[] {
  const requests = new Set<number>();
  const out: FeedItem[] = [];
  for (const item of feed) {
    if (item.fileRef?.artifactId === artifactId) {
      if (item.seq != null) requests.add(item.seq);
      out.push(item);
      continue;
    }
    if (item.author === "agent" && item.sourceSeq != null && requests.has(item.sourceSeq)) out.push(item);
  }
  return out;
}

/** What a request says about the selection: its label, content, kind and
 *  unit keys. Where it shows (rect) is layout for the selection action,
 *  never sent (docs/prd/2026-10-14-select-edit-review.md rule 1). */
export function requestSelection(sel: Selection): FileSelection {
  return {
    label: sel.label,
    ...(sel.detail ? { detail: sel.detail } : {}),
    ...(sel.kind ? { kind: sel.kind } : {}),
    ...(sel.keys?.length ? { keys: [...sel.keys] } : {}),
  };
}

/** A thread entry: a feed row, or the change card of a version a request
 *  produced. */
export type ThreadEntry =
  | { readonly kind: "item"; readonly item: FeedItem }
  | { readonly kind: "edit"; readonly version: ArtifactVersion; readonly request: FeedItem };

/** The thread with a change card after each request's last row for every
 *  version it produced (version.sourceSeq = the request's seq), in version
 *  order (docs/prd/2026-10-14-select-edit-review.md rule 4). */
export function threadWithEdits(thread: readonly FeedItem[], versions: readonly ArtifactVersion[]): ThreadEntry[] {
  const bySeq = new Map<number, ArtifactVersion[]>();
  for (const v of [...versions].sort((a, b) => a.version - b.version)) {
    if (!v.sourceSeq) continue;
    bySeq.set(v.sourceSeq, [...(bySeq.get(v.sourceSeq) ?? []), v]);
  }
  // The last row of each request: the request itself, or its latest reply.
  const last = new Map<number, number>();
  const requests = new Map<number, FeedItem>();
  thread.forEach((item, i) => {
    const seq = item.fileRef && item.seq != null ? item.seq : item.sourceSeq;
    if (seq == null || !bySeq.has(seq)) return;
    if (item.fileRef && item.seq != null) requests.set(seq, item);
    last.set(seq, i);
  });
  const out: ThreadEntry[] = [];
  thread.forEach((item, i) => {
    out.push({ kind: "item", item });
    for (const [seq, at] of last) {
      const request = requests.get(seq);
      if (at !== i || !request) continue;
      for (const version of bySeq.get(seq) ?? []) out.push({ kind: "edit", version, request });
    }
  });
  return out;
}
