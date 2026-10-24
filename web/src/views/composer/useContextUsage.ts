import { useMemo } from "react";
import { agentIdentity } from "@/lib/roomAgents";
import { latestContext, type ContextUsage } from "@/lib/contextUsage";
import type { LocalAgent } from "@/lib/useLocalAgent";
import { explicitRecipient } from "@/views/composer/recipient";
import type { RoomAgent } from "@/views/composer/types";
import type { FeedItem } from "@/views/room/types";

// useContextUsage is how full the addressed agent's conversation is: its
// newest reply that says. The agent is resolved with the precedence a send
// uses (an in-text @mention, else the picked recipient, else my selected local
// target), by the full identity (owner, core, provider): two agents of one
// owner, or one core under two owners, are two conversations.
export function useContextUsage({
  feed,
  text,
  roomAgents,
  recipient,
  subject,
  agent,
}: {
  readonly feed: readonly FeedItem[] | undefined;
  readonly text: string;
  readonly roomAgents: readonly RoomAgent[];
  readonly recipient: RoomAgent | null;
  readonly subject: string | null;
  readonly agent: Pick<LocalAgent, "targets" | "target">;
}): ContextUsage | null {
  return useMemo(() => {
    if (!feed) return null;
    const pick = explicitRecipient(text, roomAgents, recipient);
    const target = pick ? null : agent.targets.find((t) => t.id === agent.target);
    const key = pick
      ? agentIdentity({ ownerId: pick.ownerId, core: pick.core || undefined, provider: pick.provider })
      : target && subject
        ? agentIdentity({ ownerId: subject, core: target.core || undefined, provider: target.provider })
        : "";
    return latestContext(feed, key);
  }, [feed, text, roomAgents, recipient, subject, agent.targets, agent.target]);
}
