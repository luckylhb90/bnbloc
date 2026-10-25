import { useMemo, useState, type ReactNode } from "react";
import { isPreviewRoom, joinNeedsInviteCode } from "@/lib/apiModels";
import type { LocalAgent } from "@/lib/useLocalAgent";
import { fileAskRequests } from "@/lib/team";
import type { SendOptions } from "@/views/room/useRoomMessageActions";
import type { useRoomData } from "@/views/room/useRoomData";
import type { FeedItem } from "@/views/room/types";
import { RoomComposer } from "@/views/room/RoomComposer";
import type { ComposerFileContext } from "@/views/composer/composerShared";
import type { FileAsk, FileFocus } from "./FileAskPanel";

// How a file's Ask AI panel gets its composer (docs/prd/2026-10-14-ai-document-editing.md
// and docs/prd/2026-10-14-file-prompts-and-compare.md rule 4): the room's own
// composer, its sends naming the file, over the room's feed. The room
// window and a popped-out preview window each own their room data (feed,
// members, agents, send) and hand it here, so there is one place that builds
// the panel's composer and one that follows the file in front of the room.

// No typing line in a file panel's composer.
const NO_TYPERS: Record<string, number> = {};

export interface RoomFileAskInputs {
  readonly roomId: string;
  readonly channelId: string | null;
  readonly feed: FeedItem[];
  readonly data: ReturnType<typeof useRoomData>;
  readonly readOnly: boolean;
  readonly agent: LocalAgent;
  readonly subject: string | null;
  readonly online: string[];
  readonly send: (text: string, opts: SendOptions) => Promise<boolean>;
}

export function useRoomFileAsk(i: RoomFileAskInputs): {
  readonly fileAsk: FileAsk;
  /** The focused file's context for the room composer's starters (undefined
   *  with no file in front). */
  readonly roomFileContext: ComposerFileContext | undefined;
} {
  const { roomId, channelId, feed, data, readOnly, agent, subject, online, send } = i;
  const { room, roomAgents } = data;
  // The file in front of the room (the workbench's active file tab).
  const [fileFocus, setFileFocus] = useState<FileFocus | null>(null);
  const roomFileContext = useMemo<ComposerFileContext | undefined>(
    () =>
      fileFocus
        ? { file: fileFocus.file, onPick: (choice) => fileAskRequests.queue(roomId, { artifactId: fileFocus.artifactId, choice }) }
        : undefined,
    [fileFocus, roomId],
  );
  const fileAsk = useMemo<FileAsk>(
    () => ({
      roomId,
      feed,
      focus: setFileFocus,
      composer: (ref, options): ReactNode => (
        <RoomComposer
          key={`file:${ref.artifactId}`}
          join={isPreviewRoom(room) ? { needsCode: joinNeedsInviteCode(room), joining: data.joining, onJoin: (code) => void data.join(code) } : undefined}
          readOnly={readOnly}
          agent={agent}
          subject={subject}
          roomId={roomId}
          channelId={channelId}
          typingUsers={NO_TYPERS}
          roomAgents={roomAgents}
          roomMembers={data.members}
          online={online}
          feed={feed}
          onSend={async (text, opts) => {
            const fileRef = options.prepare ? await options.prepare(ref) : ref;
            return fileRef ? send(text, { ...opts, fileRef }) : false;
          }}
          platformAgents={data.platform}
          modelCatalogs={data.modelCatalogs}
          variant="file"
          fileContext={{ file: options.file, selectionKind: options.selectionKind }}
          threadLength={options.threadLength}
          pick={options.pick}
        />
      ),
    }),
    [roomId, feed, room, readOnly, agent, subject, channelId, roomAgents, data, online, send],
  );
  return { fileAsk, roomFileContext };
}
