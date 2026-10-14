// The file's Ask AI thread (docs/prd/2026-10-14-ai-document-editing.md rule
// 1): not a chat of its own, but the room conversation's messages about one
// file and what answered them. Pure (no React), unit-tested under node.

import type { FeedItem } from "../room/types.ts";
import type { Selection } from "../../../previewer-sdk/src/protocol.ts";
import type { FileSelection } from "../../lib/wire.gen.ts";

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
