// Shared composer types + the agent-handle helper. Extracted from Composer.tsx
// so the composer's sub-pickers (AgentPicker, ModelPicker) can import them
// without a circular dependency on the Composer module.

// A referenceable feed entry the composer can quote (a member message or an
// agent output already visible in the room).
export interface Referenceable {
  id: number;
  author: string;
  text: string;
}

// Optional agent tuning threaded to the send path: an explicit model and/or
// reasoning effort. Both optional — omitting either keeps the agent's default.
export interface AgentOptions {
  model?: string;
  reasoningEffort?: string;
}

// RoomAgent is a selectable agent recipient in the room.
export interface RoomAgent {
  ownerId: string;
  provider: string;
  // The daemon core hosting this agent. Undefined for
  // borrowed agents advertised without a core; grouped under "other".
  core?: string;
  self: boolean; // true when it is one of my own agents
  // Borrowed drives of this agent run in its owner's agent sandbox (shell
  // commands in a VM, the CLI confined to the room workspace).
  sandboxed?: boolean;
  // A platform agent (a super admin's published agent added to the room):
  // driven like a borrowed agent, listed in its own picker group.
  platform?: boolean;
  // A platform agent's server-side default model (its routed host's policy):
  // the model picker shows it as the default; the bridge applies it when the
  // send carries no explicit model.
  defaultModel?: string;
  // Optional per-send model + reasoning tuning selected in the composer. When
  // present the send path forwards it to the driving agent.
  agentOptions?: AgentOptions;
}

// A mention handle is core-qualified when the agent advertises a core, so two
// cores exposing the same owner/provider (e.g. both "maya/codex") stay distinct
// in the mention list and in recipient resolution. Legacy agents without a core
// keep the bare owner/provider form.
export function agentHandle(a: { ownerId: string; provider: string; core?: string }): string {
  return a.core ? `${a.core}/${a.ownerId}/${a.provider}` : `${a.ownerId}/${a.provider}`;
}
