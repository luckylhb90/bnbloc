import { useCallback, useMemo } from "react";
import { JoinPanel } from "@/views/room/JoinPanel";
import { api } from "@/lib/api";
import { dedupeMembers } from "@/lib/format";
import { Composer, type ComposerFileContext } from "@/views/Composer";
import type { PromptChoice } from "@/lib/promptPacks";
import type { RoomAgent } from "@/views/composer/types";
import { agentHandle } from "@/lib/roomAgents";
import type { LocalAgent } from "@/lib/useLocalAgent";
import { isSelf, type FeedItem } from "@/views/room/types";
import type { SendOptions } from "@/views/room/useRoomMessageActions";
import type { RoomPlatformAgents } from "@/views/room/useRoomPlatformAgents";
import type { RoomModelCatalogs } from "@/lib/agentCatalogs";
import { showStarterPrompts } from "@/lib/onboarding";
import { useDisplayNames } from "@/lib/useDisplayName";

// RoomComposer is the room footer: the join panel (when previewing an unjoined
// room), a read-only note (a visitor: lib/roomRoles mayChat), or the
// live-typing indicator plus the message composer. It is
// flat and full-bleed at every width: one top rule, no outer gutter, no card;
// only the safe-area insets (home indicator, landscape notch) pad it.
export function RoomComposer({
  join,
  readOnly = false,
  agent,
  subject,
  roomId,
  channelId,
  typingUsers,
  roomAgents,
  roomMembers,
  online,
  feed,
  onSend,
  platformAgents,
  modelCatalogs,
  working = 0,
  variant = "room",
  fileContext,
  pick,
  threadLength,
}: {
  // Set while the person is only reading the room (a preview): how they join.
  readonly join?: { readonly needsCode: boolean; readonly joining: boolean; readonly onJoin: (code?: string) => void };
  // The member may follow the room but not post (a visitor).
  readonly readOnly?: boolean;
  readonly agent: LocalAgent;
  readonly subject: string | null;
  readonly roomId: string;
  readonly channelId: string | null;
  readonly typingUsers: Record<string, number>;
  readonly roomAgents: RoomAgent[];
  readonly roomMembers: string[];
  readonly online: string[];
  readonly feed: FeedItem[];
  readonly onSend: (text: string, opts: SendOptions) => Promise<boolean>;
  readonly platformAgents?: Pick<RoomPlatformAgents, "choices" | "add" | "route" | "commit">;
  // The room's owner-keyed model catalogs (the model picker's source).
  readonly modelCatalogs?: RoomModelCatalogs;
  // Agents working in the room now (an empty feed shows starter prompts
  // only while nothing is under way).
  readonly working?: number;
  // "people": the people chat's composer (no agent picker, people-only);
  // "file": a file's Ask AI panel (its sends name the file).
  readonly variant?: "room" | "people" | "file";
  // The file the conversation is about (Composer fileContext) and a
  // suggestion handed over to insert.
  readonly fileContext?: ComposerFileContext;
  readonly pick?: { readonly choice: PromptChoice; readonly nonce: number };
  // The length of the conversation the starters are for, when it is not
  // the feed (a file panel's thread).
  readonly threadLength?: number;
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
  // Starter prompts: an empty conversation with an agent to send to (a
  // member's, a local target, or a platform agent with an online host).
  const showStarters = showStarterPrompts({
    feedLength: threadLength ?? feed.length,
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
      {join ? (
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 px-4 py-3">
          <p className="text-sm text-[var(--color-fg-muted)]">
            You are reading this room before joining it. {join.needsCode ? "Enter its invite code to chat and collaborate." : "Join to chat and collaborate."}
          </p>
          <JoinPanel needsCode={join.needsCode} joining={join.joining} onJoin={join.onJoin} />
        </div>
      ) : readOnly ? (
        <p className="px-4 py-3 text-sm text-[var(--color-fg-muted)]">
          You can follow this room, but your role here is read-only: you can't post or ask agents.
        </p>
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
            fileContext={fileContext}
            pick={pick}
            feed={feed}
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
