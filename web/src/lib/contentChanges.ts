import type { ContentKind } from "./wire.gen.ts";

// contentChanges is the one owner of "a content component changed, so the
// lists that show it read it again" (docs/bugs/2026-10-21-library-stale-
// after-delete-and-find-a-way-navigation.md). Every content mutation of the
// client goes through an API module (contentApi, contentSharingApi,
// promptPacksApi, workGalleryApi), and each announces itself here once the
// server took it (changing); every list of content subscribes
// (lib/useContentChanged.ts) instead of each view reloading what it
// remembers to. A change made outside this page (another tab, another
// member, the API) reaches an open Library through its visible re-read
// (views/library/useContentLibrary.ts).

/** What changed: the content kind when the call knows it, null when it
 *  does not (an id-only call, a use): then every list reads again. */
export interface ContentChange {
  readonly kind: ContentKind | null;
}

type Listener = (change: ContentChange) => void;

const listeners = new Set<Listener>();

export const contentChanges = {
  subscribe(listener: Listener): () => void {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  },
};

/** announceContentChange tells every list. A listener that throws is
 *  logged and never keeps the others from hearing it. */
export function announceContentChange(kind: ContentKind | null): void {
  const change: ContentChange = { kind };
  for (const listener of [...listeners]) {
    try {
      listener(change);
    } catch (e) {
      console.error("bnbloc content change listener failed:", e);
    }
  }
}

/** touches reports whether a change concerns a list of these kinds (a
 *  change of an unknown kind concerns every list). */
export function touches(change: ContentChange, kinds: readonly ContentKind[]): boolean {
  return change.kind === null || kinds.includes(change.kind);
}

/** changing runs a mutation and announces it once the server took it; a
 *  failed one changed nothing and announces nothing. kind is the kind, or
 *  how to read it from the answer (a save's detail names its component). */
export async function changing<T>(run: Promise<T>, kind: ContentKind | null | ((answer: T) => ContentKind | null)): Promise<T> {
  const answer = await run;
  announceContentChange(typeof kind === "function" ? kind(answer) : kind);
  return answer;
}
