// Pure tab model of the preview workbench (no React, no DOM; unit-tested
// under node). useWorkbench wires it to React state and per-room
// browser storage (lib/storageKeys.ts).
//
// A tab is either one artifact (optionally pinned to a version, optionally
// opened with a specific viewer — the "Open with…" choice) or the room's
// "All deliverables" list. Pinned tabs form a group at the front: they are
// skipped by "close others", never evicted, and reorder only among
// themselves.

import { StorageKey, keyFor } from "./storageKeys.ts";

export const DELIVERABLES_TAB = "deliverables";
/** Upper bound of open tabs; the oldest unpinned, inactive tab is evicted. */
export const MAX_TABS = 12;

export interface DeliverablesFilter {
  /** Only artifacts produced by this message (a chat card's "View all"). */
  readonly sourceSeq?: number;
}

export interface WorkbenchTab {
  /** "a:<artifactId>" or DELIVERABLES_TAB. */
  readonly key: string;
  readonly artifactId?: string;
  /** A specific version; absent = follow the current version. */
  readonly version?: number;
  readonly pinned: boolean;
  /** Viewer override ("Open with…"); absent = best match. */
  readonly viewerId?: string;
  readonly filter?: DeliverablesFilter;
}

export interface WorkbenchState {
  readonly tabs: readonly WorkbenchTab[];
  readonly active: string | null;
}

export const EMPTY_WORKBENCH: WorkbenchState = { tabs: [], active: null };

export type WorkbenchAction =
  | { type: "open"; artifactId: string; version?: number }
  | { type: "openDeliverables"; filter?: DeliverablesFilter }
  | { type: "activate"; key: string }
  | { type: "close"; key: string }
  | { type: "closeOthers"; key: string }
  | { type: "pin"; key: string; pinned: boolean }
  | { type: "move"; key: string; toIndex: number }
  | { type: "setVersion"; key: string; version?: number }
  | { type: "setViewer"; key: string; viewerId?: string }
  | { type: "reset"; state: WorkbenchState };

export function artifactTabKey(artifactId: string): string {
  return `a:${artifactId}`;
}

// Pinned tabs first, each group keeping its relative order.
function grouped(tabs: readonly WorkbenchTab[]): WorkbenchTab[] {
  return [...tabs.filter((t) => t.pinned), ...tabs.filter((t) => !t.pinned)];
}

function insertAfterActive(state: WorkbenchState, tab: WorkbenchTab): WorkbenchTab[] {
  const at = state.tabs.findIndex((t) => t.key === state.active);
  const tabs = [...state.tabs];
  // A new tab lands right after the active one (or at the end), never inside
  // the pinned group.
  const pinnedCount = tabs.filter((t) => t.pinned).length;
  const index = at >= 0 ? Math.max(at + 1, pinnedCount) : tabs.length;
  tabs.splice(index, 0, tab);
  return tabs;
}

function evict(tabs: WorkbenchTab[], keep: string): WorkbenchTab[] {
  const out = [...tabs];
  while (out.length > MAX_TABS) {
    const victim = out.findIndex((t) => !t.pinned && t.key !== keep);
    if (victim < 0) break;
    out.splice(victim, 1);
  }
  return out;
}

function neighbourOf(tabs: readonly WorkbenchTab[], key: string): string | null {
  const at = tabs.findIndex((t) => t.key === key);
  if (at < 0) return null;
  const next = tabs[at + 1] ?? tabs[at - 1];
  return next ? next.key : null;
}

function update(state: WorkbenchState, key: string, patch: (t: WorkbenchTab) => WorkbenchTab): WorkbenchState {
  if (!state.tabs.some((t) => t.key === key)) return state;
  return { ...state, tabs: state.tabs.map((t) => (t.key === key ? patch(t) : t)) };
}

export function workbenchReducer(state: WorkbenchState, action: WorkbenchAction): WorkbenchState {
  switch (action.type) {
    case "open": {
      const key = artifactTabKey(action.artifactId);
      const existing = state.tabs.find((t) => t.key === key);
      if (existing) {
        const next = action.version === undefined ? state : update(state, key, (t) => ({ ...t, version: action.version }));
        return { ...next, active: key };
      }
      const tab: WorkbenchTab = { key, artifactId: action.artifactId, pinned: false, version: action.version };
      return { tabs: evict(insertAfterActive(state, tab), key), active: key };
    }
    case "openDeliverables": {
      const existing = state.tabs.find((t) => t.key === DELIVERABLES_TAB);
      if (existing) {
        return { ...update(state, DELIVERABLES_TAB, (t) => ({ ...t, filter: action.filter })), active: DELIVERABLES_TAB };
      }
      const tab: WorkbenchTab = { key: DELIVERABLES_TAB, pinned: false, filter: action.filter };
      return { tabs: evict(insertAfterActive(state, tab), DELIVERABLES_TAB), active: DELIVERABLES_TAB };
    }
    case "activate":
      return state.tabs.some((t) => t.key === action.key) ? { ...state, active: action.key } : state;
    case "close": {
      if (!state.tabs.some((t) => t.key === action.key)) return state;
      const active = state.active === action.key ? neighbourOf(state.tabs, action.key) : state.active;
      return { tabs: state.tabs.filter((t) => t.key !== action.key), active };
    }
    case "closeOthers": {
      if (!state.tabs.some((t) => t.key === action.key)) return state;
      return { tabs: state.tabs.filter((t) => t.pinned || t.key === action.key), active: action.key };
    }
    case "pin": {
      const next = update(state, action.key, (t) => ({ ...t, pinned: action.pinned }));
      if (next === state) return state;
      // Re-group: the tab sits on the pinned/unpinned boundary — a newly
      // pinned tab joins the end of the pinned group, an unpinned one the
      // start of the unpinned group.
      const tab = next.tabs.find((t) => t.key === action.key)!;
      const rest = next.tabs.filter((t) => t.key !== action.key);
      return { ...next, tabs: [...rest.filter((t) => t.pinned), tab, ...rest.filter((t) => !t.pinned)] };
    }
    case "move": {
      const from = state.tabs.findIndex((t) => t.key === action.key);
      if (from < 0) return state;
      const tab = state.tabs[from];
      const rest = state.tabs.filter((t) => t.key !== action.key);
      // Clamp into the tab's own group (pinned tabs stay in front).
      const pinnedCount = rest.filter((t) => t.pinned).length;
      const lo = tab.pinned ? 0 : pinnedCount;
      const hi = tab.pinned ? pinnedCount : rest.length;
      const to = Math.min(hi, Math.max(lo, Math.trunc(action.toIndex)));
      if (to === from) return state;
      rest.splice(to, 0, tab);
      return { ...state, tabs: rest };
    }
    case "setVersion":
      return update(state, action.key, (t) => ({ ...t, version: action.version }));
    case "setViewer":
      return update(state, action.key, (t) => ({ ...t, viewerId: action.viewerId }));
    case "reset":
      return action.state;
  }
}

/** localStorage key of a room's workbench. */
export function workbenchStorageKey(roomId: string): string {
  return keyFor(StorageKey.workbench, roomId);
}

/** Serializes the persisted part of the state. */
export function serializeWorkbench(state: WorkbenchState): string {
  return JSON.stringify({ v: 1, tabs: state.tabs, active: state.active });
}

/** Parses persisted state, dropping anything malformed (storage is user-
 *  editable and outlives code versions). */
export function parseWorkbench(raw: string | null | undefined): WorkbenchState {
  if (!raw) return EMPTY_WORKBENCH;
  let data: unknown;
  try {
    data = JSON.parse(raw);
  } catch {
    return EMPTY_WORKBENCH;
  }
  if (!data || typeof data !== "object") return EMPTY_WORKBENCH;
  const rec = data as { v?: unknown; tabs?: unknown; active?: unknown };
  if (rec.v !== 1 || !Array.isArray(rec.tabs)) return EMPTY_WORKBENCH;
  const tabs: WorkbenchTab[] = [];
  const seen = new Set<string>();
  for (const item of rec.tabs) {
    const tab = parseTab(item);
    if (!tab || seen.has(tab.key)) continue;
    seen.add(tab.key);
    tabs.push(tab);
  }
  const ordered = grouped(tabs).slice(0, MAX_TABS);
  const active = typeof rec.active === "string" && ordered.some((t) => t.key === rec.active) ? rec.active : ordered[0]?.key ?? null;
  return { tabs: ordered, active };
}

function parseTab(item: unknown): WorkbenchTab | null {
  if (!item || typeof item !== "object") return null;
  const t = item as Record<string, unknown>;
  const pinned = t.pinned === true;
  const viewerId = typeof t.viewerId === "string" && t.viewerId ? t.viewerId : undefined;
  if (t.key === DELIVERABLES_TAB) {
    const f = t.filter as { sourceSeq?: unknown } | undefined;
    const seq = f && typeof f.sourceSeq === "number" && Number.isInteger(f.sourceSeq) && f.sourceSeq > 0 ? f.sourceSeq : undefined;
    return { key: DELIVERABLES_TAB, pinned, filter: seq ? { sourceSeq: seq } : undefined };
  }
  if (typeof t.artifactId !== "string" || !t.artifactId || t.key !== artifactTabKey(t.artifactId)) return null;
  const version = typeof t.version === "number" && Number.isInteger(t.version) && t.version > 0 ? t.version : undefined;
  return { key: t.key, artifactId: t.artifactId, pinned, version, viewerId };
}
