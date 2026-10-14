import { useCallback, useEffect, useState } from "react";
import { CornersInIcon, CornersOutIcon, DownloadSimpleIcon } from "@phosphor-icons/react";
import { fileCategory, fileTypeLabel } from "@/lib/artifactFiles";
import { errorText } from "@/lib/errors";
import { formatBytes } from "@/lib/format";
import { saveBlob } from "@/lib/saveBlob";
import type { AttachmentTabFile } from "@/lib/workbenchTabs";
import { preferredViewerFor } from "@/lib/viewerPreference";
import { useToast } from "@/ui/Toast";
import "@/views/artifact/builtin";
import { ArtifactBody, resolveViewer } from "@/views/artifact/ArtifactBody";
import { useViewerRegistry } from "@/views/artifact/registry";
import { attachmentFacts, attachmentFileInfo } from "@/views/artifact/fileInfo";
import { OpenWithMenu } from "@/views/artifact/OpenWithMenu";
import type { ViewerToolbarItem, ViewerToolbarValue } from "@/views/artifact/registry";
import { ViewerToolbar } from "@/views/artifact/toolbar";
import { attachmentBlob } from "@/views/room/useAttachmentUrl";
import { ACTION } from "./ArtifactTabView";
import { categoryIcons } from "./fileIcons";

// AttachmentTabView: a file a member attached to a chat message, opened in
// the preview workbench with the same viewers as a deliverable. An
// attachment is one immutable file, not an artifact: no versions, no share
// link, no live state, no stored thumbnail (ArtifactBody gets no artifact
// id). Its bytes come through a per-attachment ticket.


export function AttachmentTabView({
  roomId,
  attachment,
  viewerId,
  onViewer,
  fullscreen,
  onToggleFullscreen,
}: {
  readonly roomId: string;
  readonly attachment: AttachmentTabFile;
  readonly viewerId?: string;
  readonly onViewer: (viewerId: string | undefined) => void;
  readonly fullscreen?: boolean;
  readonly onToggleFullscreen?: () => void;
}) {
  const toast = useToast();
  useViewerRegistry();
  const file = attachmentFileInfo(attachment);
  const facts = attachmentFacts(attachment);
  const viewer = resolveViewer(file, viewerId ?? preferredViewerFor(file), false);
  const [state, setState] = useState<{ blob: Blob | null; error: string }>({ blob: null, error: "" });
  useEffect(() => {
    let cancelled = false;
    setState({ blob: null, error: "" });
    attachmentBlob(roomId, attachment.id, attachment.contentType)
      .then((blob) => {
        if (!cancelled) setState({ blob, error: "" });
      })
      .catch((e: unknown) => {
        if (!cancelled) setState({ blob: null, error: errorText(e, "Could not load this file.") });
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, attachment.id, attachment.contentType]);
  const [tools, setTools] = useState<{ items: readonly ViewerToolbarItem[]; emit: (id: string, value: ViewerToolbarValue) => void }>({
    items: [],
    emit: () => {},
  });
  const onToolbar = useCallback(
    (items: readonly ViewerToolbarItem[], emit: (id: string, value: ViewerToolbarValue) => void) => setTools({ items, emit }),
    [],
  );
  const download = async () => {
    try {
      saveBlob(state.blob ?? (await attachmentBlob(roomId, attachment.id, attachment.contentType)), attachment.filename);
    } catch {
      toast.error("Download failed");
    }
  };
  const Icon = categoryIcons[fileCategory(facts)];

  return (
    <div className="@container flex h-full min-h-0 flex-col" data-attachment-tab={attachment.id}>
      <div className="flex shrink-0 flex-col gap-1 border-b border-[var(--color-border)] px-2 py-1.5">
        <div className="flex min-w-0 items-center gap-1.5">
          <Icon size={16} className="ml-1 shrink-0 text-[var(--color-accent)]" aria-hidden />
          <span className="min-w-0 truncate text-sm font-medium text-[var(--color-fg)]" title={attachment.filename}>
            {attachment.filename}
          </span>
          <span className="shrink-0 rounded-full border border-[var(--color-border)] px-1.5 text-[10px] uppercase tracking-wide text-[var(--color-fg-subtle)]">
            {fileTypeLabel(facts)}
          </span>
          <span className="hidden shrink-0 text-[11px] text-[var(--color-fg-subtle)] tabular @xs:inline">{formatBytes(attachment.size)} · attachment</span>
          <span className="ml-auto flex shrink-0 items-center">
            <button type="button" className={ACTION} onClick={() => void download()} title="Download" aria-label="Download">
              <DownloadSimpleIcon size={16} />
            </button>
            {onToggleFullscreen && (
              <button
                type="button"
                className={ACTION}
                onClick={onToggleFullscreen}
                title={fullscreen ? "Exit fullscreen (Esc)" : "Fullscreen"}
                aria-label={fullscreen ? "Exit fullscreen" : "Fullscreen"}
                aria-pressed={!!fullscreen}
              >
                {fullscreen ? <CornersInIcon size={16} /> : <CornersOutIcon size={16} />}
              </button>
            )}
          </span>
        </div>
        {(tools.items.length > 0 || viewer) && (
          <div className="flex min-w-0 flex-wrap items-center gap-1">
            <ViewerToolbar items={tools.items} onAction={tools.emit} className="ml-auto" />
            <OpenWithMenu file={file} currentViewerId={viewer?.id} onChoose={(id) => onViewer(id ?? undefined)} className={tools.items.length === 0 ? "ml-auto" : undefined} />
          </div>
        )}
      </div>
      <div className="relative min-h-0 flex-1 overflow-hidden" role="region" aria-label={attachment.filename}>
        {state.error ? (
          <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Could not load this file: {state.error}</p>
        ) : !state.blob ? (
          <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Loading preview…</p>
        ) : (
          <ArtifactBody roomId={roomId} artifactId="" file={file} blob={state.blob} viewer={viewer} onDownload={() => void download()} onToolbar={onToolbar} />
        )}
      </div>
    </div>
  );
}
