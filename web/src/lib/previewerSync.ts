import { frameTicketStale, parsePreviewerCatalog, type PreviewerDescriptor } from "./previewerFrame.ts";

// Where the viewer registry learns which previewers are installed, and when it
// re-reads them (the pure part of views/artifact/remotePreviewers: no registry,
// no network, so it is tested under node). One source is active at a time: the
// signed-in app reads the member catalog; the public share page reads the list
// the link's creator can offer for the shared file. Refreshing a near-expiry
// frame ticket reads the same active source.

/** A place the previewer list comes from: the raw body of a catalog answer
 *  (`{components: [...]}`), parsed here by parsePreviewerCatalog. */
export interface PreviewerSource {
  read(): Promise<unknown>;
}

export interface PreviewerSync {
  /** Makes `next` the active source; the returned function restores the one it
   *  replaced (only if `next` is still the active one). A read still in flight
   *  for the replaced source is ignored when it answers. */
  setSource(next: PreviewerSource): () => void;
  /** Re-reads the active source and applies the list. Concurrent callers of
   *  one source share one read; a failure rejects and applies nothing. */
  sync(): Promise<void>;
  /** The descriptor of a previewer whose frame ticket is still valid for a new
   *  frame, re-reading the active source first when it is near expiry;
   *  undefined when the source no longer lists it. */
  fresh(name: string, nowMs?: number): Promise<PreviewerDescriptor | undefined>;
}

export function createPreviewerSync(
  initial: PreviewerSource,
  apply: (descriptors: readonly PreviewerDescriptor[]) => void,
  current: (name: string) => PreviewerDescriptor | undefined,
): PreviewerSync {
  let source = initial;
  let inflight: { readonly source: PreviewerSource; readonly done: Promise<void> } | null = null;

  const sync = (): Promise<void> => {
    if (inflight && inflight.source === source) return inflight.done;
    const mine = source;
    const done: Promise<void> = mine
      .read()
      .then((body) => {
        if (source === mine) apply(parsePreviewerCatalog(body));
      })
      .finally(() => {
        if (inflight?.done === done) inflight = null;
      });
    inflight = { source: mine, done };
    return done;
  };

  return {
    setSource(next) {
      const previous = source;
      source = next;
      inflight = null;
      return () => {
        if (source !== next) return;
        source = previous;
        inflight = null;
      };
    },
    sync,
    async fresh(name, nowMs = Date.now()) {
      const have = current(name);
      if (have && !frameTicketStale(have, nowMs)) return have;
      await sync();
      return current(name);
    },
  };
}

/** Resolves when the promise settles (either way) or after ms, whichever is
 *  first: a lookup the page must not wait on forever. Never rejects. */
export function settledWithin(promise: Promise<unknown>, ms: number): Promise<void> {
  return new Promise<void>((done) => {
    const timer = setTimeout(done, ms);
    const finish = () => {
      clearTimeout(timer);
      done();
    };
    promise.then(finish, finish);
  });
}
