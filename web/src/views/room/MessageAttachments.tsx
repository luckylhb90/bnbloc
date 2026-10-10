import { useCallback, useRef, useState } from "react";
import { DownloadSimpleIcon, FileIcon, XIcon } from "@phosphor-icons/react";
import { api, type ChatAttachment } from "@/lib/api";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/cn";
import { useModalLayer } from "@/ui/useModalLayer";

const INLINE_IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp"]);

export function isInlineImage(contentType: string): boolean {
  return INLINE_IMAGE_TYPES.has(contentType.split(";")[0].trim().toLowerCase());
}

type LightboxImage = {
  readonly url: string;
  readonly filename: string;
};

export function MessageAttachments({
  roomId,
  attachments,
  className,
}: {
  readonly roomId: string;
  readonly attachments: readonly ChatAttachment[] | undefined;
  readonly className?: string;
}) {
  const [lightbox, setLightbox] = useState<LightboxImage | null>(null);
  // The lightbox is a modal layer: closing it restores focus to its trigger.
  const closeLightbox = useCallback(() => setLightbox(null), []);

  if (!attachments || attachments.length === 0) return null;
  return (
    <>
      <div className={cn("flex flex-col gap-2", className)}>
        {attachments.map((attachment) => {
          const url = api.attachmentUrl(roomId, attachment.id);
          // Only raster images render inline; everything else (SVG included,
          // which the gateway serves as a download) is a download link.
          if (isInlineImage(attachment.contentType)) {
            return (
              <button
                key={attachment.id}
                type="button"
                onClick={() => setLightbox({ url, filename: attachment.filename })}
                className="tactile block w-fit overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)] hover:border-[var(--color-border-strong)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
                title={`Open ${attachment.filename}`}
                aria-label={`Open ${attachment.filename}`}
              >
                <img
                  src={url}
                  alt={attachment.filename}
                  width={240}
                  height={240}
                  className="max-h-60 max-w-60 object-contain"
                  loading="lazy"
                />
              </button>
            );
          }
          return (
            <a
              key={attachment.id}
              href={url}
              download={attachment.filename}
              className="tactile flex w-fit max-w-full items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2 text-sm text-[var(--color-fg)] hover:border-[var(--color-border-strong)] hover:bg-[var(--color-elevated)]"
            >
              <FileIcon size={17} className="shrink-0 text-[var(--color-fg-subtle)]" />
              <span className="min-w-0">
                <span className="block truncate font-medium">{attachment.filename}</span>
                <span className="block tabular text-xs text-[var(--color-fg-subtle)]">
                  {formatBytes(attachment.size)}
                </span>
              </span>
            </a>
          );
        })}
      </div>
      {lightbox && <AttachmentLightbox image={lightbox} onClose={closeLightbox} />}
    </>
  );
}

function AttachmentLightbox({
  image,
  onClose,
}: {
  readonly image: LightboxImage;
  readonly onClose: () => void;
}) {
  const surfaceRef = useRef<HTMLDivElement | null>(null);
  useModalLayer(surfaceRef, { onEscape: onClose });

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
