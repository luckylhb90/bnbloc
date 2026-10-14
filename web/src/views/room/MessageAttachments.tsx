import { createContext, useCallback, useContext, useRef, useState } from "react";
import { DownloadSimpleIcon, FileIcon, XIcon } from "@phosphor-icons/react";
import type { ChatAttachment } from "@/lib/api";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/cn";
import type { AttachmentTabFile } from "@/lib/workbenchTabs";
import { useModalLayer } from "@/ui/useModalLayer";
import "@/views/artifact/builtin";
import { selectViewer, useViewerRegistry } from "@/views/artifact/registry";
import { useAttachmentUrl } from "@/views/room/useAttachmentUrl";
import { attachmentFileInfo } from "@/views/artifact/fileInfo";

const INLINE_IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp"]);

export function isInlineImage(contentType: string): boolean {
  return INLINE_IMAGE_TYPES.has(contentType.split(";")[0].trim().toLowerCase());
}

// Opens an attachment in the room's preview workbench; absent where there is
// no workbench (the attachment is then a download link).
const AttachmentOpenerContext = createContext<((file: AttachmentTabFile) => void) | null>(null);
export const AttachmentOpenerProvider = AttachmentOpenerContext.Provider;

function tabFile(a: ChatAttachment): AttachmentTabFile {
  return { id: a.id, filename: a.filename, contentType: a.contentType, size: a.size };
}

// Attachment links carry a per-attachment ticket, never the session
// (useAttachmentUrl). A raster image opens in a lightbox; any other file a
// viewer can show (documents, slides, sheets, PDF, text…) opens in the
// preview workbench; the rest download.
export function MessageAttachments({
  roomId,
  attachments,
  className,
}: {
  readonly roomId: string;
  readonly attachments: readonly ChatAttachment[] | undefined;
  readonly className?: string;
}) {
  const [lightbox, setLightbox] = useState<ChatAttachment | null>(null);
  // The lightbox is a modal layer: closing it restores focus to its trigger.
  const closeLightbox = useCallback(() => setLightbox(null), []);

  if (!attachments || attachments.length === 0) return null;
  return (
    <>
      <div className={cn("flex flex-col gap-2", className)}>
        {attachments.map((attachment) => (
          <AttachmentItem key={attachment.id} roomId={roomId} attachment={attachment} onOpen={setLightbox} />
        ))}
      </div>
      {lightbox && <AttachmentLightbox roomId={roomId} attachment={lightbox} onClose={closeLightbox} />}
    </>
  );
}

function AttachmentItem({
  roomId,
  attachment,
  onOpen,
}: {
  readonly roomId: string;
  readonly attachment: ChatAttachment;
  readonly onOpen: (a: ChatAttachment) => void;
}) {
  const { url, ensure, onDownload } = useAttachmentUrl(roomId, attachment.id, attachment.filename);
  const openInWorkbench = useContext(AttachmentOpenerContext);
  useViewerRegistry();
  // Only raster images render inline (SVG included is served as a download).
  if (isInlineImage(attachment.contentType)) {
    return (
      <button
        type="button"
        // The lightbox opens on a valid ticket (renewed if this one expired).
        onClick={() => void ensure().then(() => onOpen(attachment), () => {})}
        className="tactile block w-fit overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)] hover:border-[var(--color-border-strong)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
        title={`Open ${attachment.filename}`}
        aria-label={`Open ${attachment.filename}`}
      >
        {url ? (
          <img
            src={url}
            alt={attachment.filename}
            width={240}
            height={240}
            className="max-h-60 max-w-60 object-contain"
            loading="lazy"
          />
        ) : (
          <span className="block size-60" aria-hidden />
        )}
      </button>
    );
  }
  const label = (
    <>
      <FileIcon size={17} className="shrink-0 text-[var(--color-fg-subtle)]" />
      <span className="min-w-0">
        <span className="block truncate font-medium">{attachment.filename}</span>
        <span className="block tabular text-xs text-[var(--color-fg-subtle)]">{formatBytes(attachment.size)}</span>
      </span>
    </>
  );
  const file = tabFile(attachment);
  if (openInWorkbench && selectViewer(attachmentFileInfo(file))) {
    return (
      <span className="flex w-fit max-w-full items-stretch overflow-hidden rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] text-sm text-[var(--color-fg)] hover:border-[var(--color-border-strong)]">
        <button
          type="button"
          onClick={() => openInWorkbench(file)}
          className="tactile flex min-w-0 items-center gap-2 px-3 py-2 text-left hover:bg-[var(--color-elevated)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-accent)]/60"
          title={`Preview ${attachment.filename}`}
          aria-label={`Preview ${attachment.filename}`}
        >
          {label}
        </button>
        <a
          href={url ?? undefined}
          download={attachment.filename}
          onClick={onDownload}
          className="tactile grid shrink-0 place-items-center border-l border-[var(--color-border)] px-2.5 text-[var(--color-fg-subtle)] hover:bg-[var(--color-elevated)] hover:text-[var(--color-fg)]"
          title={`Download ${attachment.filename}`}
          aria-label={`Download ${attachment.filename}`}
        >
          <DownloadSimpleIcon size={15} />
        </a>
      </span>
    );
  }
  return (
    <a
      href={url ?? undefined}
      download={attachment.filename}
      onClick={onDownload}
      className="tactile flex w-fit max-w-full items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2 text-sm text-[var(--color-fg)] hover:border-[var(--color-border-strong)] hover:bg-[var(--color-elevated)]"
    >
      {label}
    </a>
  );
}

function AttachmentLightbox({
  roomId,
  attachment,
  onClose,
}: {
  readonly roomId: string;
  readonly attachment: ChatAttachment;
  readonly onClose: () => void;
}) {
  const surfaceRef = useRef<HTMLDivElement | null>(null);
  useModalLayer(surfaceRef, { onEscape: onClose });
  const { url, onDownload } = useAttachmentUrl(roomId, attachment.id, attachment.filename);
  const image = { url: url ?? undefined, filename: attachment.filename };

  return (
    <div
      ref={surfaceRef}
      tabIndex={-1}
      role="dialog"
      aria-modal="true"
      aria-label={`${image.filename} preview`}
      className="fixed inset-0 z-50 flex items-center justify-center p-4 text-[var(--color-fg)]"
    >
      <button
        type="button"
        tabIndex={-1}
        className="absolute inset-0 cursor-zoom-out bg-black/80 backdrop-blur-sm animate-in"
        onClick={onClose}
        aria-label="Close image preview"
      />
      <div className="animate-in relative z-10 flex max-h-[96vh] max-w-[96vw] flex-col items-center gap-3">
        <div className="pointer-events-auto overflow-hidden rounded-[var(--radius-xl)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] shadow-[var(--shadow-diffuse)]">
          <img
            src={image.url}
            alt={image.filename}
            className="max-h-[90vh] max-w-[90vw] object-contain"
          />
        </div>
        <div className="flex max-w-[90vw] items-center gap-2 rounded-full border border-[var(--color-border)] bg-[var(--color-surface)]/90 px-3 py-2 text-xs shadow-[var(--shadow-pop)] backdrop-blur-sm">
          <span className="min-w-0 truncate text-[var(--color-fg-muted)]">{image.filename}</span>
          <a
            href={image.url}
            download={image.filename}
            onClick={onDownload}
            className="tactile inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-1 font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent-soft)]"
          >
            <DownloadSimpleIcon size={13} />
            Download
          </a>
        </div>
      </div>
      <button
        autoFocus
        type="button"
        onClick={onClose}
        className="tactile absolute right-4 top-4 z-20 grid size-9 place-items-center rounded-full border border-[var(--color-border-strong)] bg-[var(--color-surface)]/90 text-[var(--color-fg-muted)] shadow-[var(--shadow-pop)] backdrop-blur-sm hover:bg-[var(--color-elevated)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
        aria-label="Close image preview"
      >
        <XIcon size={18} />
      </button>
    </div>
  );
}
