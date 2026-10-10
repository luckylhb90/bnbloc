import { useCallback, useEffect, useRef, useState } from "react";

// A minimal server-state query hook: cache + in-flight dedupe + revalidation,
// with no new dependency. It exists to collapse the repeated
// useEffect+loading+error+race boilerplate scattered across the views, while
// leaving the api client (and its silent re-auth/retry) as the transport and
// leaving SSE (useRoomStream) untouched — SSE is a live stream, not a cacheable
// request, and must never flow through here.
//
// This is intentionally not react-query: the SPA ships in three embedding forms
// and every added dependency must be re-validated in all three, so a ~100-line
// hook that covers the read patterns we actually have is the right size.

interface Entry<T> {
  value?: T;
  error?: unknown;
  promise?: Promise<void>;
  ts: number; // last successful fetch time
  subscribers: Set<() => void>;
}

const cache = new Map<string, Entry<unknown>>();

function entryFor<T>(key: string): Entry<T> {
  let e = cache.get(key) as Entry<T> | undefined;
  if (!e) {
    e = { ts: 0, subscribers: new Set() };
    cache.set(key, e as Entry<unknown>);
  }
  return e;
}

// runFetch coalesces concurrent callers on one key onto a single in-flight
// promise, then notifies every subscriber when it settles.
function runFetch<T>(key: string, fetcher: () => Promise<T>): Promise<void> {
  const e = entryFor<T>(key);
  if (e.promise) return e.promise;
  e.promise = fetcher().then(
    (value) => {
      e.value = value;
      e.error = undefined;
      e.ts = Date.now();
      e.promise = undefined;
      e.subscribers.forEach((fn) => fn());
    },
    (error) => {
      e.error = error;
      e.promise = undefined;
      e.subscribers.forEach((fn) => fn());
    },
  );
  return e.promise;
}

export interface QueryResult<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
  refetch: () => void;
}

// useQuery reads server state for a stable key. Passing key=null disables the
// query (for dependent/conditional fetches). staleMs controls how long a cached
// value is served without a background refetch; 0 means always revalidate on
// mount/key change.
export function useQuery<T>(
  key: string | null,
  fetcher: () => Promise<T>,
  opts: { staleMs?: number } = {},
): QueryResult<T> {
  const staleMs = opts.staleMs ?? 0;
  const fetcherRef = useRef(fetcher);
  fetcherRef.current = fetcher;

  const [, forceRender] = useState(0);
  const rerender = useCallback(() => forceRender((n) => n + 1), []);

  useEffect(() => {
    if (key === null) return;
    const e = entryFor<T>(key);
    e.subscribers.add(rerender);
    const fresh = e.ts > 0 && Date.now() - e.ts < staleMs;
    if (!fresh && !e.promise) void runFetch(key, () => fetcherRef.current());
    return () => {
      e.subscribers.delete(rerender);
    };
  }, [key, staleMs, rerender]);

  const refetch = useCallback(() => {
    if (key === null) return;
    const e = entryFor<T>(key);
    e.ts = 0;
    void runFetch(key, () => fetcherRef.current());
  }, [key]);

  if (key === null) {
    return { data: undefined, error: undefined, loading: false, refetch };
  }
  const e = entryFor<T>(key);
  return {
    data: e.value,
    error: e.error,
    loading: e.promise !== undefined && e.value === undefined,
    refetch,
  };
}
