import { api, type Channel } from "@/lib/api";
import type { RoomAccessEnd } from "@/lib/roomGone";
import type { LocalAgent } from "@/lib/useLocalAgent";
import type { RoomChannel } from "@/lib/wire.gen";
import type { RoomAgent } from "@/views/composer/types";
import { ForumView } from "@/views/room/ForumView";
import { ThreadPanel } from "@/views/room/ThreadPanel";
import type { FeedItem } from "@/views/room/types";
import type { RoomThreads } from "@/views/room/useRoomThreads";

// The room view's threads (docs/prd/2026-10-28-channels-and-threads.md §2.2,
// §2.1c), kept out of RoomView: the side panel of one thread, with what it
// needs of the channel it hangs under (its root, a forum's vocabulary, the
// parent to post a closing summary to), and a forum channel's main view.

export function RoomThreadPanel({
  roomId,
  thread: th,
  channelId,
  channel,
  feed,
  subject,
  joinedGen,
  agent,
  roomAgents,
  members,
  online,
  mayPost,
  onChanged,
  onClose,
  onGone,
}: {
  readonly roomId: string;
  readonly thread: RoomChannel;
  // The channel the room view shows, its listing and its feed: the thread's
  // root and its forum settings when it hangs under that channel.
  readonly channelId: string | null;
  readonly channel: Channel | null | undefined;
  readonly feed: readonly FeedItem[];
  readonly subject: string | null;
  readonly joinedGen: number;
  readonly agent: LocalAgent;
  readonly roomAgents: RoomAgent[];
  readonly members: string[];
  readonly online: string[];
  readonly mayPost: boolean;
  readonly onChanged: () => void;
  readonly onClose: () => void;
  readonly onGone: (reason: RoomAccessEnd) => void;
}) {
  const underCurrent = th.thread?.parentId === channelId;
  return (
    <ThreadPanel
      roomId={roomId}
      thread={th}
      subject={subject}
      joinedGen={joinedGen}
      agent={agent}
      roomAgents={roomAgents}
      members={members}
      online={online}
      mayPost={mayPost}
      parentSettings={underCurrent ? channel?.settings : undefined}
      root={underCurrent ? feed.find((m) => m.seq === th.thread?.rootSeq) : undefined}
      sendToParent={async (parentId, rootSeq, text) => {
        await api.sendChat(roomId, { text, recipient: null, channelId: parentId, attachmentIds: [], replyTo: rootSeq }, crypto.randomUUID());
      }}
      onChanged={onChanged}
      onClose={onClose}
      onGone={onGone}
    />
  );
}

export function RoomForum({
  roomId,
  channel,
  threads,
  unreadByChannel,
  mayPost,
  beforeOpen,
}: {
  readonly roomId: string;
  readonly channel: Channel;
  readonly threads: RoomThreads;
  readonly unreadByChannel: Readonly<Record<string, number>>;
  readonly mayPost: boolean;
  // Opening a thread closes the people chat (one side panel at a time).
  readonly beforeOpen: () => void;
}) {
  return (
    <ForumView
      roomId={roomId}
      channel={channel}
      threads={threads.channelThreads}
      unreadByChannel={unreadByChannel}
      mayPost={mayPost}
      onOpen={(th) => {
        beforeOpen();
        threads.openChannel(th);
      }}
      onChanged={(opened) => {
        if (opened) {
          beforeOpen();
          threads.reloadAndOpen(opened);
        } else threads.reload();
      }}
    />
  );
}
