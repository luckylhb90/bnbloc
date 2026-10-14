// The composer's saved agent per room+channel (server agent prefs, the only
// per-room store). An agent is (owner, core, provider): a harness alone
// names several members' agents, and restoring one by (core, provider)
// landed a member on another member's agent and started a borrow (web H9).
// Pure, so it is unit-tested (agentPrefs.test.ts); useAgentPrefs drives it.
import type { AgentTarget } from "./apiModels.ts";
import type { RoomAgent } from "../views/composer/types.ts";
import { agentIdentity } from "./roomAgents.ts";

// What the server stores for one room+channel (GET/PUT …/agent-prefs).
export interface SavedAgent {
  readonly ownerId: string;
  readonly core: string;
  readonly provider: string;
}

// The selection to restore: one of my own local targets, or a room agent
// (another member's, a platform agent, or my own advertised one when no
// local target matches).
export type RestoredAgent = { readonly ownTargetId: string } | { readonly recipient: RoomAgent };

export function restoreAgent(
  saved: Partial<SavedAgent>,
  roomAgents: readonly RoomAgent[],
  targets: readonly Pick<AgentTarget, "id" | "core" | "provider">[],
  subject: string | null,
): RestoredAgent | null {
  if (!saved.ownerId || !saved.provider) return null;
  const want = agentIdentity({ ownerId: saved.ownerId, core: saved.core || undefined, provider: saved.provider });
  const mine = !!subject && agentIdentity({ ownerId: subject, core: saved.core || undefined, provider: saved.provider }) === want;
  // My own agent prefers my local target: it is what I drive directly.
  if (mine) {
    const local = targets.find((t) => t.provider === saved.provider && (t.core || "") === (saved.core || ""));
    if (local) return { ownTargetId: local.id };
  }
  const agent = roomAgents.find((a) => agentIdentity(a) === want);
  return agent ? { recipient: agent } : null;
}

// savedAgent is what the composer's current pick stores: the picked room
// agent, else my own selected local target.
export function savedAgent(
  recipient: RoomAgent | null,
  ownTarget: Pick<AgentTarget, "core" | "provider"> | undefined,
  subject: string | null,
): SavedAgent | null {
  if (recipient) return { ownerId: recipient.ownerId, core: recipient.core ?? "", provider: recipient.provider };
  if (ownTarget?.provider && subject) return { ownerId: subject, core: ownTarget.core || "", provider: ownTarget.provider };
  return null;
}

// offeredChoice is a stored model or reasoning pick as the composer uses it
// right now (shown in the picker, sent with the message). The stored pick is
// the member's: only their own pick or a restore changes it, never this.
// It is used as stored while the agent's catalog is unknown or empty (a
// core restarting, a catalog still loading), and set aside ("" = the
// agent's default) only while a loaded, non-empty catalog lacks it — kept
// for when the catalog offers it again.
export function offeredChoice(stored: string, choices: readonly string[] | undefined): string {
  if (!stored) return "";
  if (!choices || choices.length === 0) return stored;
  return choices.includes(stored) ? stored : "";
}
