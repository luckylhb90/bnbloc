import { useCallback, useMemo } from "react";
import { api } from "@/lib/api";
import { Button } from "@/ui/Button";
import { dedupeMembers } from "@/lib/format";
import { Composer } from "@/views/Composer";
import { agentHandle, type RoomAgent } from "@/views/composer/types";
import type { LocalAgent } from "@/lib/useLocalAgent";
import { isSelf, type FeedItem } from "@/views/room/types";
import type { SendOptions } from "@/views/room/useRoomMessageActions";
import type { RoomPlatformAgents } from "@/views/room/useRoomPlatformAgents";
import type { RoomModelCatalogs } from "@/lib/agentCatalogs";
import { showStarterPrompts } from "@/lib/onboarding";
import { useDisplayNames } from "@/lib/useDisplayName";

// RoomComposer is the room footer: either the join panel (when previewing an
// unjoined room) or the live-typing indicator plus the message composer. It is
// flat and full-bleed at every width: one top rule, no outer gutter, no card;
// only the safe-area insets (home indicator, landscape notch) pad it.
export function RoomComposer({
  roomPreview,
  agent,
  subject,
  roomId,
  channelId,
  typingUsers,
  roomAgents,
  roomMembers,
  online,
  feed,
  joining,
  onSend,
  onJoin,
  platformAgents,
  modelCatalogs,
  working = 0,
  variant = "room",
}: {
  readonly roomPreview: boolean;
  readonly agent: LocalAgent;
  readonly subject: string | null;
  readonly roomId: string;
  readonly channelId: string | null;
  readonly typingUsers: Record<string, number>;
  readonly roomAgents: RoomAgent[];
  readonly roomMembers: string[];
  readonly online: string[];
  readonly feed: FeedItem[];
  readonly joining: boolean;
  readonly onSend: (text: string, opts: SendOptions) => Promise<boolean>;
  readonly onJoin: () => void;
  readonly platformAgents?: Pick<RoomPlatformAgents, "choices" | "add" | "route" | "commit">;
  // The room's owner-keyed model catalogs (the model picker's source).
  readonly modelCatalogs?: RoomModelCatalogs;
  // Agents working in the room now (an empty feed shows starter prompts
  // only while nothing is under way).
  readonly working?: number;
  // "people": the people chat's composer (no agent picker, people-only).
  readonly variant?: "room" | "people";
}) {
  // Stable inputs for the composer's mention list and references.
  const members = useMemo(
    () => dedupeMembers([...roomAgents.map((a) => agentHandle(a)), ...roomMembers, ...online]),
    [roomAgents, roomMembers, online],
  );
  const referenceable = useMemo(
    () =>
      feed
        .filter((m) => m.text && m.status !== "pending" && m.status !== "failed")
        .slice(-50)
        .map((m) => ({ id: m.id, author: m.author, text: m.text })),
    [feed],
  );
  // Starter prompts: an empty feed with an agent to send to (a member's, a
  // local target, or a platform agent with an online host).
  const showStarters = showStarterPrompts({
    feedLength: feed.length,
    working,
    people: variant === "people",
    agentAvailable:
      roomAgents.some((a) => !a.platform) || agent.targets.length > 0 || !!platformAgents?.choices.some((c) => c.online),
  });
  const onTyping = useCallback(
    () => void api.roomTyping(roomId, channelId ?? undefined).catch(() => {}),
    [roomId, channelId],
  );
  return (
    <div data-tour={variant === "room" ? "composer" : undefined} className="pad-safe pad-safe-b w-full border-t border-[var(--color-border)] focus-within:border-[var(--color-border-strong)]">
      {roomPreview ? (
        <div className="flex items-center justify-between gap-4 px-4 py-3">
          <p className="text-sm text-[var(--color-fg-muted)]">
            You are viewing a shared room. Join to chat and collaborate.
          </p>
          <Button variant="primary" onClick={onJoin} disabled={joining}>
            {joining ? "Joining…" : "Join room"}
          </Button>
        </div>
      ) : (
        <>
          <TypingLine typers={Object.keys(typingUsers).filter((u) => !isSelf(u, subject))} />
          <Composer
            roomId={roomId}
            channelId={channelId}
            agent={agent}
            onSend={onSend}
            onTyping={onTyping}
            subject={subject}
            members={members}
            roomAgents={roomAgents}
            referenceable={referenceable}
            platformAgents={platformAgents}
            modelCatalogs={modelCatalogs}
            showStarters={showStarters}
            variant={variant}
          />
        </>
      )}
    </div>
  );
}

// TypingLine names who is typing, by display name.
function TypingLine({ typers }: { readonly typers: readonly string[] }) {
  const nameOf = useDisplayNames(typers);
  if (typers.length === 0) return null;
  const label =
    typers.length === 1
      ? `${nameOf(typers[0])} is typing…`
      : typers.length === 2
        ? `${nameOf(typers[0])} and ${nameOf(typers[1])} are typing…`
        : `${typers.length} people are typing…`;
  return <p className="px-4 pt-1.5 text-xs text-[var(--color-fg-subtle)]">{label}</p>;
}
