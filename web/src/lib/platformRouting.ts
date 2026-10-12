// Client-side host routing of platform agents
// (docs/prd/2026-10-10-platform-agents-v2-onboarding.md §4). A send to a
// platform agent picks one of its hosts:
//   1. the host this room used last for this agent, if online and not busier
//      than the least busy online host by BUSIER_BY or more jobs;
//   2. otherwise the online host with the fewest queued jobs.
// The recipient stays {ownerId: host, core, provider}; the bridge claim path
// is unchanged. The last host per (member, room, agent) is remembered in
// localStorage so a room keeps talking to the same machine (same agent
// session) while it is healthy.
import type { PlatformAgentRef, PlatformHost } from "./platformAgents.ts";
import { sameUser } from "./format.ts";

export const BUSIER_BY = 3;


// pickHost returns the host a send goes to, or null when no host is online.
// Ties keep the listed order (the server's order is stable).
export function pickHost(hosts: readonly PlatformHost[], lastHost: string | null): PlatformHost | null {
  const online = hosts.filter((h) => h.online);
  if (online.length === 0) return null;
  const queued = (h: PlatformHost) => (Number.isFinite(h.queued) && h.queued > 0 ? h.queued : 0);
  const leastBusy = online.reduce((best, h) => (queued(h) < queued(best) ? h : best));
  const last = lastHost ? online.find((h) => sameUser(h.hostUserId, lastHost)) : undefined;
  if (last && queued(last) - queued(leastBusy) < BUSIER_BY) return last;
  return leastBusy;
}

const STORAGE_KEY = "bnbloc.platformHosts";
// Bounded: the oldest remembered rooms are dropped first.
const MAX_ENTRIES = 200;

type Storage = Pick<globalThis.Storage, "getItem" | "setItem">;

function entryKey(subject: string, roomId: string, agent: PlatformAgentRef): string {
  return `${subject}|${roomId}|${(agent.core ?? "").toLowerCase()}/${agent.provider.toLowerCase()}`;
}

function readAll(storage: Storage): Record<string, string> {
  try {
    const parsed: unknown = JSON.parse(storage.getItem(STORAGE_KEY) ?? "{}");
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as Record<string, string>) : {};
  } catch {
    return {};
  }
}

// readLastHost is the host this member's last send in the room went to for
// the agent (null when none is remembered or storage is unavailable).
export function readLastHost(storage: Storage | null, subject: string | null, roomId: string, agent: PlatformAgentRef): string | null {
  if (!storage || !subject) return null;
  const v = readAll(storage)[entryKey(subject, roomId, agent)];
  return typeof v === "string" && v ? v : null;
}

// rememberHost records the host a send went to. Best-effort: a full or
// unavailable storage only loses the preference.
export function rememberHost(storage: Storage | null, subject: string | null, roomId: string, agent: PlatformAgentRef, host: string): void {
  if (!storage || !subject || !host) return;
  const all = readAll(storage);
  const key = entryKey(subject, roomId, agent);
  delete all[key];
  all[key] = host;
  const keys = Object.keys(all);
  for (const k of keys.slice(0, Math.max(0, keys.length - MAX_ENTRIES))) delete all[k];
  try {
    storage.setItem(STORAGE_KEY, JSON.stringify(all));
  } catch {
    // quota / private mode: routing falls back to the least busy host
  }
}

// browserStorage is localStorage when the page may use it.
export function browserStorage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null;
  }
}
