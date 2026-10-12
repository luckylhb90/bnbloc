import { useCallback, useEffect, useRef, useState } from "react";
import { gatewayBase } from "./config.ts";

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
  // The fetcher the latest subscriber registered (invalidateQueries reruns it).
  fetcher?: () => Promise<T>;
}

const cache = new Map<string, Entry<unknown>>();

// QueryScope is whose data a query reads: the signed-in member and tenant
// (and, implicitly, the gateway). Two members, tenants or gateways never
// share a cached answer.
export interface QueryScope {
  readonly subject: string | null;
  readonly tenant: string | null;
}

// queryKey is the one cache key of a named query in a scope; null (the
// query is off) without a signed-in member.
export function queryKey(name: string, scope: QueryScope): string | null {
  if (!scope.subject) return null;
  return [gatewayBase(), scope.tenant ?? "", scope.subject, name].join("\u0000");
}

// publicQueryKey is the key of a query whose answer is the same for every
// member of a gateway (the release hub).
export function publicQueryKey(name: string): string {
  return [gatewayBase(), "", "", name].join("\u0000");
}

// clearQueryCache forgets every cached answer (sign-out, tenant switch), so
// the next member never sees the previous one's data.
export function clearQueryCache(): void {
  cache.clear();
}

// invalidateQueries refetches every cached query whose name starts with
// prefix now (after a write that changes it), so every reader follows at
// once instead of at its next poll.
export function invalidateQueries(prefix: string): void {
  for (const [key, e] of cache) {
    const name = key.slice(key.lastIndexOf("\u0000") + 1);
    if (!name.startsWith(prefix)) continue;
    e.ts = 0;
    if (e.subscribers.size > 0 && e.fetcher) void runFetch(key, e.fetcher);
  }
}

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
  // No answer yet (first load).
  loading: boolean;
  // A request is in flight (first load or a revalidation).
  fetching: boolean;
  // refetch reloads now; the promise settles when the answer is in.
  refetch: () => Promise<void>;
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
    e.fetcher = () => fetcherRef.current();
    const fresh = e.ts > 0 && Date.now() - e.ts < staleMs;
    if (!fresh && !e.promise) void runFetch(key, () => fetcherRef.current());
    return () => {
      e.subscribers.delete(rerender);
    };
  }, [key, staleMs, rerender]);

  const refetch = useCallback(async () => {
    if (key === null) return;
    const e = entryFor<T>(key);
    e.ts = 0;
    await runFetch(key, () => fetcherRef.current());
  }, [key]);

  if (key === null) {
    return { data: undefined, error: undefined, loading: false, fetching: false, refetch };
  }
  const e = entryFor<T>(key);
  return {
    data: e.value,
    error: e.error,
    loading: e.promise !== undefined && e.value === undefined,
    fetching: e.promise !== undefined,
    refetch,
  };
}
