// Browser storage: every key the client keeps, in one table, and the one
// guarded accessor (web L1; boundary rule 3). A key that holds a server id
// (a room, a target, a ref) or a member's choice is scoped to the member on
// this gateway (memberKey), so another member, or the same member on another
// gateway, never inherits it. Parts are joined with ":" everywhere. (No
// imports: lib/config.ts reads its override through this file at load.)

export const StorageKey = {
  // The signed-in session (lib/sessionGrant.ts) and the gateway override.
  session: "bnbloc.session",
  gatewayOverride: "bnbloc.gatewayOverride",
  // The desktop sign-in nonce (lib/desktop.ts).
  ssoNonce: "bnbloc.ssoNonce",
  // A super admin's acting tenant, per member (lib/session.ts).
  actingTenant: "bnbloc.actingTenant",
  privacyMode: "bnbloc.privacyMode",
  // Dev-only manual local core address, and the selected agent target.
  localAgentUrl: "bnbloc.localAgentUrl",
  localAgentToken: "bnbloc.localAgentToken",
  agentTarget: "bnbloc.agentTarget",
  // Sidecar sign-ins started this session (sessionStorage).
  sidecarSignInStarted: "bnbloc.sidecarSignInStarted",
  viewerChoice: "bnbloc.viewerChoice.v1",
  // The composer reference handed between windows, and the last room.
  composerRef: "bnbloc.composerRef",
  lastRoom: "bnbloc.lastRoom",
  platformHosts: "bnbloc.platformHosts",
  // Layout choices.
  dockShare: "bnbloc.dockShare",
  sidebarCollapsed: "bnbloc.sidebarCollapsed",
  peopleChatWidth: "bnbloc.peopleChatWidth",
  deliverablesLayout: "bnbloc.deliverables.layout",
  // Per-member and per-room state.
  welcomeDismissed: "bnbloc.welcomeDismissed",
  desktopHintDismissed: "bnbloc.desktopHint.dismissed",
  onboarding: "bnbloc.onboarding.v1",
  workshopRoom: "bnbloc.workshopRoom",
  handoffBanner: "bnbloc.handoffBanner",
  templateStart: "bnbloc.templateStart",
  workbench: "bnbloc.workbench",
} as const;

export type StorageKeyName = (typeof StorageKey)[keyof typeof StorageKey];

// keyFor is base qualified by parts (a room id, a member): "base:part:…".
export function keyFor(base: StorageKeyName, ...parts: readonly string[]): string {
  return [base, ...parts].join(":");
}

// memberKey is base scoped to one member on one gateway (gatewayBase(),
// "" for same-origin), plus parts.
export function memberKey(base: StorageKeyName, member: { readonly gateway: string; readonly subject: string }, ...parts: readonly string[]): string {
  return keyFor(base, member.gateway || "same-origin", member.subject, ...parts);
}

// browserStorage is localStorage (or sessionStorage) when the page may use
// it: null where storage is blocked or absent (private mode, a worker).
export function browserStorage(area: "local" | "session" = "local"): Storage | null {
  try {
    if (typeof window === "undefined") return null;
    return area === "local" ? window.localStorage : window.sessionStorage;
  } catch {
    return null;
  }
}

// readStored is a stored value, "" when absent or unreadable.
export function readStored(key: string, area: "local" | "session" = "local"): string {
  try {
    return browserStorage(area)?.getItem(key) ?? "";
  } catch {
    return "";
  }
}

// writeStored stores value ("" removes the key). A full or blocked storage
// keeps the value in memory only: callers never fail on it.
export function writeStored(key: string, value: string, area: "local" | "session" = "local"): void {
  try {
    const s = browserStorage(area);
    if (!s) return;
    if (value) s.setItem(key, value);
    else s.removeItem(key);
  } catch {
    /* quota / private mode */
  }
}
