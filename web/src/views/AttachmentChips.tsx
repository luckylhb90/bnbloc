import { CircleNotchIcon, FileIcon, ImageIcon, XIcon } from "@phosphor-icons/react";
import { formatBytes } from "@/lib/format";
import { cn } from "@/lib/cn";

export type AttachmentChipStatus = "ready" | "uploading" | "uploaded" | "error";

export interface AttachmentChipItem {
  readonly id: string;
  readonly filename: string;
  readonly contentType: string;
  readonly size: number;
  readonly previewUrl?: string;
  readonly status: AttachmentChipStatus;
  readonly error?: string;
}

export function AttachmentChips({
  attachments,
  onRemove,
}: {
  readonly attachments: readonly AttachmentChipItem[];
  readonly onRemove: (id: string) => void;
}) {
  if (attachments.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-1.5 px-3 pt-3">
      {attachments.map((attachment) => {
        const isImage = attachment.contentType.startsWith("image/");
        const label = statusLabel(attachment.status);
        return (
          <span
            key={attachment.id}
            className={cn(
              "flex max-w-[18rem] items-center gap-2 rounded-full border bg-[var(--color-surface-2)] py-1 pl-1.5 pr-1.5 text-xs",
              attachment.status === "error"
                ? "border-[var(--color-danger)]/50"
                : "border-[var(--color-border)]",
            )}
          >
            {isImage && attachment.previewUrl ? (
              <img
                src={attachment.previewUrl}
                alt=""
                className="size-6 rounded-[var(--radius)] object-cover"
              />
            ) : (
              <span className="grid size-6 place-items-center rounded-[var(--radius)] bg-[var(--color-elevated)] text-[var(--color-fg-subtle)]">
                {isImage ? <ImageIcon size={14} /> : <FileIcon size={14} />}
              </span>
            )}
            <span className="min-w-0 flex-1">
              <span className="block truncate font-medium text-[var(--color-fg)]">
                {attachment.filename}
              </span>
              <span
                className={cn(
                  "block truncate tabular text-[0.65rem]",
                  attachment.status === "error"
                    ? "text-[var(--color-danger)]"
                    : "text-[var(--color-fg-subtle)]",
                )}
              >
                {attachment.error ?? `${formatBytes(attachment.size)}${label ? ` · ${label}` : ""}`}
              </span>
            </span>
            {attachment.status === "uploading" && (
              <CircleNotchIcon size={13} className="animate-spin text-[var(--color-accent)]" />
            )}
            <button
              type="button"
              onClick={() => onRemove(attachment.id)}
              className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
              title="Remove attachment"
              aria-label={`Remove ${attachment.filename}`}
            >
              <XIcon size={12} />
            </button>
          </span>
        );
      })}
    </div>
  );
}

function statusLabel(status: AttachmentChipStatus): string {
  if (status === "uploading") return "Uploading";
  if (status === "uploaded") return "Uploaded";
  return "";
}
