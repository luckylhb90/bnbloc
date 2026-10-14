import { FileTextIcon } from "@phosphor-icons/react";
import type { FileRef } from "@/lib/wire.gen";
import { artifactLinks } from "@/lib/team";

// FileRefTag marks a request sent from a file's Ask AI panel
// (payload.fileRef): which file and version it is about, and the part the
// member pointed at. Clicking it opens that version in the workbench.
export function FileRefTag({ roomId, fileRef, className }: { readonly roomId: string; readonly fileRef?: FileRef; readonly className?: string }) {
  if (!fileRef) return null;
  const name = `${fileRef.name ?? "a file"} v${fileRef.version}`;
  const label = fileRef.selection ? `${name} · ${fileRef.selection.label}` : name;
  return (
    <button
      type="button"
      data-file-ref
      onClick={() => artifactLinks.queue(roomId, { artifactId: fileRef.artifactId, version: fileRef.version })}
      title={`About ${label}: open it`}
      className={
        "tactile inline-flex min-w-0 max-w-full items-center gap-1 text-[0.68rem] text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)] " +
        (className ?? "")
      }
    >
      <FileTextIcon size={12} className="shrink-0" aria-hidden="true" />
      <span className="truncate">About {label}</span>
    </button>
  );
}
