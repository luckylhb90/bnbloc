import { useCallback, useState, type Dispatch, type SetStateAction } from "react";

// useScopedState is useState whose value belongs to one scope (e.g. the
// active channel). When `scope` changes the value restarts from `init()` in
// the same render — no stale frame of the previous scope's data — and every
// setter is bound to the scope it was created in: a write from a closure of
// an older scope (an in-flight load, a timer) is dropped instead of landing in
// the new scope. That makes "old channel's response applied to the new
// channel" impossible by construction rather than by per-call cancel flags.
export function useScopedState<T>(scope: string, init: () => T): [T, Dispatch<SetStateAction<T>>] {
  const [slot, setSlot] = useState(() => ({ scope, value: init() }));
  let current = slot;
  if (slot.scope !== scope) {
    current = { scope, value: init() };
    setSlot(current);
  }
  const set = useCallback<Dispatch<SetStateAction<T>>>(
    (next) =>
      setSlot((s) =>
        s.scope !== scope
          ? s
          : {
              scope,
              value: typeof next === "function" ? (next as (prev: T) => T)(s.value) : next,
            },
      ),
    [scope],
  );
  return [current.value, set];
}
