import { createContext, useContext, useEffect, useMemo, useRef, type ReactNode } from "react";
import { XIcon } from "@phosphor-icons/react";
import type { FileRef } from "@/lib/wire.gen";
import { Message } from "@/views/room/Message";
import type { FeedItem } from "@/views/room/types";
import { fileThread } from "./fileAsk";

// The Ask AI panel beside a file in the workbench
// (docs/prd/2026-10-14-ai-document-editing.md M1): the room conversation's
// requests about this file and their replies, and a composer whose sends
// name the file. The room owns both (RoomView provides them): the panel
// is a view, not a second chat. Where no room provides them (a popped-out
// window, a share page) there is no panel.

export interface FileAsk {
  readonly roomId: string;
  readonly feed: readonly FeedItem[];
  /** The room composer, its sends naming ref. */
  readonly composer: (ref: FileRef) => ReactNode;
}

const FileAskContext = createContext<FileAsk | null>(null);
export const FileAskProvider = FileAskContext.Provider;

export function useFileAsk(): FileAsk | null {
  return useContext(FileAskContext);
}

export function FileAskPanel({
  ask,
  fileRef,
  onClose,
}: {
  readonly ask: FileAsk;
  readonly fileRef: FileRef;
  readonly onClose: () => void;
}) {
  const thread = useMemo(() => fileThread(ask.feed, fileRef.artifactId), [ask.feed, fileRef.artifactId]);
  const endRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    endRef.current?.scrollIntoView({ block: "end" });
  }, [thread.length]);
  return (
    <aside
      aria-label={`Ask AI about ${fileRef.name ?? "this file"}`}
      // Beside the preview in a wide tab, under it in a narrow one.
      className="flex h-[45%] min-h-0 w-full shrink-0 flex-col border-t border-[var(--color-border)] bg-[var(--color-surface)] @2xl:h-full @2xl:w-[min(26rem,45%)] @2xl:border-l @2xl:border-t-0"
    >
      <div className="flex shrink-0 items-center gap-2 border-b border-[var(--color-border)] px-3 py-2">
        <span className="min-w-0 flex-1 truncate text-xs font-medium text-[var(--color-fg)]">Ask AI about {fileRef.name ?? "this file"}</span>
        <button
          type="button"
          onClick={onClose}
          className="tactile grid size-7 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
          aria-label="Close Ask AI"
          title="Close"
        >
          <XIcon size={14} />
        </button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
        {thread.length === 0 ? (
          <p className="text-xs leading-5 text-[var(--color-fg-subtle)]">
            Ask an agent to change {fileRef.name ?? "this file"}. Each change becomes a new version of it, here and in the room; any
            earlier version can be restored from the version picker.
          </p>
        ) : (
          thread.map((item) => <Message key={item.id} roomId={ask.roomId} item={item} />)
        )}
        <div ref={endRef} />
      </div>
      {/* Its own container: the composer lays out for the panel's width. */}
      <div className="@container shrink-0">{ask.composer(fileRef)}</div>
    </aside>
  );
}
