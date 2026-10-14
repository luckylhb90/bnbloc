// Component updates that wait for agent turns to finish (live turns PRD):
// the desktop keeps a core or the sandbox running on its current version
// while the bridge drives turns, and applies the new one at the first idle
// moment (apps/desktop internal_ext/update_gate.rs). These pure helpers
// read the shell's statuses and word the notes; the views stay thin.

/** One waiting update, as the Agents page shows it. */
export interface PendingUpdate {
  readonly name: string;
  readonly version: string;
  readonly waitingFor: number;
}

/** What a status of the shell carries for a waiting update. */
interface WithPending {
  readonly name: string;
  readonly pendingUpdate?: { readonly version: string; readonly waitingFor: number } | null;
}

/** pendingUpdates lists the components whose update waits, by name. */
export function pendingUpdates(statuses: readonly WithPending[]): PendingUpdate[] {
  return statuses
    .flatMap((s) => (s.pendingUpdate ? [{ name: s.name, version: s.pendingUpdate.version, waitingFor: s.pendingUpdate.waitingFor }] : []))
    .sort((a, b) => a.name.localeCompare(b.name));
}

function tasks(n: number): string {
  return n === 1 ? "1 running task" : `${n} running tasks`;
}

/** The Agents page's note for the waiting updates ("" when none). */
export function pendingUpdatesNote(pending: readonly PendingUpdate[]): string {
  if (pending.length === 0) return "";
  const what = pending.map((p) => `${p.name} ${p.version}`).join(", ");
  const waiting = Math.max(...pending.map((p) => p.waitingFor));
  const one = pending.length === 1;
  return waiting > 0
    ? `${what} ${one ? "is" : "are"} ready — waiting for ${tasks(waiting)} to finish, then ${one ? "it applies on its own" : "they apply on their own"}.`
    : `${what} ${one ? "is" : "are"} ready and will apply in a moment.`;
}

/** The warning before cutting running turns (an update applied now, or the
 *  app's own update): null when none runs. */
export function runningTurnsWarning(running: number): string | null {
  if (running <= 0) return null;
  return `Your agents are working on ${tasks(running)}. Updating now restarts them and stops ${running === 1 ? "it" : "them"} midway; their work so far stays in the room and the workspace.`;
}
