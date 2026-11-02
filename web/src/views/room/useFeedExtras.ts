import { useCallback, useMemo } from "react";
import { appLinkBase } from "@/lib/config";
import { messageLinkUrl } from "@/lib/hashRoute";
import type { ThreadSummary } from "@/lib/threads";
import { useCopy } from "@/ui/useCopy";
import type { MessageExtras } from "@/views/room/messageExtras";

// useFeedExtras is the room feed's message actions: the team's, the
// channel's threads (opened only by a member who may post), copying a
// message's link, and whether pinning is offered.
export function useFeedExtras({
  roomId,
  channelId,
  team,
  threadsByRoot,
  onOpenThread,
  mayPin,
}: {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly team: MessageExtras;
  readonly threadsByRoot: ReadonlyMap<number, ThreadSummary>;
  readonly onOpenThread: ((rootSeq: number) => void) | undefined;
  // Whether the member manages the open channel, so pins its messages.
  readonly mayPin: boolean;
}): MessageExtras {
  const copy = useCopy();
  const onCopyLink = useCallback(
    (seq: number) => void copy(messageLinkUrl(appLinkBase(), roomId, seq, channelId ?? undefined), "message link"),
    [copy, roomId, channelId],
  );
  return useMemo(() => ({ ...team, threadsByRoot, onOpenThread, onCopyLink, mayPin }), [team, threadsByRoot, onOpenThread, onCopyLink, mayPin]);
}
