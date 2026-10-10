// Signed-in devices (G4): the member's active sign-ins as GET /v1/my/sessions
// returns them, and the pure grouping the Settings page renders. A forked
// sign-in (the desktop app's background agent bridge) names the session it was
// forked from in parentId and is shown nested under that device.

export interface MySession {
  id: string;
  client: string;
  userAgent?: string;
  tenant?: string;
  createdAtMs: number;
  lastUsedAtMs: number;
  expiresAtMs: number;
  parentId?: string;
  current: boolean;
}

export interface DeviceGroup {
  session: MySession;
  agents: MySession[];
}

// clientLabel names the sign-in surface for a member.
export function clientLabel(client: string): string {
  switch (client) {
    case "web":
      return "Browser";
    case "desktop":
      return "Desktop app";
    case "cli":
      return "Command line";
    case "bridge":
      return "Desktop agent";
    default:
      return client || "Unknown client";
  }
}

// groupDevices nests each forked session under the session it was forked from
// (when that one is still listed; an orphaned fork stands on its own) and
// orders devices with the current one first, then most recently used.
export function groupDevices(sessions: readonly MySession[]): DeviceGroup[] {
  const ids = new Set(sessions.map((s) => s.id));
  const byRecent = (a: MySession, b: MySession) => b.lastUsedAtMs - a.lastUsedAtMs;
  const groups = sessions
    .filter((s) => !s.parentId || !ids.has(s.parentId))
    .map((session) => ({
      session,
      agents: sessions.filter((s) => s.parentId === session.id).sort(byRecent),
    }));
  return groups.sort((a, b) => Number(b.session.current) - Number(a.session.current) || byRecent(a.session, b.session));
}

// otherSessionCount is how many sessions "Sign out all other devices" would
// revoke: everything except the current session and the agents forked from it.
export function otherSessionCount(sessions: readonly MySession[]): number {
  const current = sessions.find((s) => s.current)?.id;
  return sessions.filter((s) => s.id !== current && (!current || s.parentId !== current)).length;
}
