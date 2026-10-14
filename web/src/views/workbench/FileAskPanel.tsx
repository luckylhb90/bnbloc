import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { CrosshairSimpleIcon, SparkleIcon, XIcon } from "@phosphor-icons/react";
import type { FileRef } from "@/lib/wire.gen";
import type { ViewerSuggestion } from "@/views/artifact/registry";
import { Message } from "@/views/room/Message";
import type { FeedItem } from "@/views/room/types";
import { fileThread } from "./fileAsk";

// The Ask AI panel beside a file in the workbench
// (docs/prd/2026-10-14-ai-document-editing.md M1): the room conversation's
// requests about this file and their replies, and a composer whose sends
// name the file. The room owns both (RoomView provides them): the panel
// is a view, not a second chat. Where no room provides them (a popped-out
// window, a share page) there is no panel.
//
// M2: the previewer may point at part of the file (fileRef.selection, a
// removable chip: the request is about that part) and offer starting
// prompts (its package's suggestions, on an empty thread; a pick fills the
// composer, it never sends).

export interface FileAsk {
  readonly roomId: string;
  readonly feed: readonly FeedItem[];
  /** The room composer, its sends naming ref; draft fills its input. */
  readonly composer: (ref: FileRef, draft?: { readonly text: string; readonly nonce: number }) => ReactNode;
}

const FileAskContext = createContext<FileAsk | null>(null);
export const FileAskProvider = FileAskContext.Provider;

export function useFileAsk(): FileAsk | null {
  return useContext(FileAskContext);
}

export function FileAskPanel({
  ask,
  fileRef,
  suggestions = [],
  onClearSelection,
  onClose,
}: {
  readonly ask: FileAsk;
  readonly fileRef: FileRef;
  readonly suggestions?: readonly ViewerSuggestion[];
  readonly onClearSelection: () => void;
  readonly onClose: () => void;
}) {
  const [draft, setDraft] = useState<{ text: string; nonce: number } | undefined>(undefined);
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
          <>
            <p className="text-xs leading-5 text-[var(--color-fg-subtle)]">
              Ask an agent to change {fileRef.name ?? "this file"}. Each change becomes a new version of it, here and in the room; any
              earlier version can be restored from the version picker.
            </p>
            {suggestions.length > 0 && (
              <div className="mt-3 flex flex-wrap gap-1.5" role="group" aria-label="Suggested requests">
                {suggestions.map((sg) => (
                  <button
                    key={sg.label}
                    type="button"
                    title={sg.prompt}
                    onClick={() => setDraft((cur) => ({ text: sg.prompt, nonce: (cur?.nonce ?? 0) + 1 }))}
                    className="tactile inline-flex max-w-full items-center gap-1 truncate rounded-full border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-1.5 text-xs text-[var(--color-fg-muted)] hover:border-[var(--color-accent)]/40 hover:bg-[var(--color-accent-soft)] hover:text-[var(--color-fg)]"
                  >
                    <SparkleIcon size={12} weight="fill" className="shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
                    <span className="truncate">{sg.label}</span>
                  </button>
                ))}
              </div>
            )}
          </>
        ) : (
          thread.map((item) => <Message key={item.id} roomId={ask.roomId} item={item} />)
        )}
        <div ref={endRef} />
      </div>
      {fileRef.selection && (
        <div className="flex shrink-0 items-center gap-1.5 border-t border-[var(--color-border)] px-3 pt-2">
          <span
            className="inline-flex min-w-0 items-center gap-1 rounded-full bg-[var(--color-accent-soft)] py-1 pl-2.5 pr-1 text-xs text-[var(--color-fg)]"
            title={fileRef.selection.detail || fileRef.selection.label}
          >
            <CrosshairSimpleIcon size={12} className="shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
            <span className="truncate">{fileRef.selection.label}</span>
            <button
              type="button"
              onClick={onClearSelection}
              className="grid size-5 shrink-0 place-items-center rounded-full text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]"
              aria-label="Ask about the whole file"
              title="Ask about the whole file"
            >
              <XIcon size={10} />
            </button>
          </span>
        </div>
      )}
      {/* Its own container: the composer lays out for the panel's width. */}
      <div className="@container shrink-0">{ask.composer(fileRef, draft)}</div>
    </aside>
  );
}
