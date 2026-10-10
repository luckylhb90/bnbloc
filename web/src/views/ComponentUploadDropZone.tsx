import { FileArchiveIcon, UploadSimpleIcon } from "@phosphor-icons/react";
import type { RefObject } from "react";
import { Card } from "@/ui/Card";

export function ComponentUploadCard({ children }: { readonly children: React.ReactNode }) {
  return (
    <Card className="p-4">
      <div className="flex items-start gap-3">
        <div className="grid size-10 shrink-0 place-items-center rounded-[var(--radius)] bg-[var(--color-accent-soft)] text-[var(--color-accent)]"><FileArchiveIcon size={20} weight="fill" /></div>
        <div className="min-w-0">
          <h3 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg)]">Publish a component</h3>
          <p className="mt-1 text-sm leading-relaxed text-[var(--color-fg-muted)]">Choose a package, configuration file, or program. We inspect it, then guide you through only the missing setup.</p>
        </div>
      </div>
      <div className="mt-4 flex flex-col gap-4">{children}</div>
    </Card>
  );
}

export function DropZone({ dragging, setDragging, inputRef, onFile }: {
  readonly dragging: boolean;
  readonly setDragging: (dragging: boolean) => void;
  readonly inputRef: RefObject<HTMLInputElement | null>;
  readonly onFile: (file: File) => Promise<void>;
}) {
  return (
    <div role="button" tabIndex={0} aria-label="Drop a file or browse"
      onClick={() => inputRef.current?.click()}
      onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); inputRef.current?.click(); } }}
      onDragOver={(event) => { event.preventDefault(); setDragging(true); }}
      onDragLeave={() => setDragging(false)}
      onDrop={(event) => { event.preventDefault(); setDragging(false); const file = event.dataTransfer.files.item(0); if (file) void onFile(file); }}
      className={dragging ? "flex cursor-pointer flex-col items-center gap-2 rounded-[var(--radius-lg)] border-[1.5px] border-dashed border-[var(--color-accent)] bg-[var(--color-accent-soft)] px-4 py-8 text-center transition-colors" : "flex cursor-pointer flex-col items-center gap-2 rounded-[var(--radius-lg)] border-[1.5px] border-dashed border-[var(--color-border-strong)] bg-[var(--color-surface)] px-4 py-8 text-center transition-colors hover:border-[var(--color-accent)]"}>
      <div className="grid size-11 place-items-center rounded-[var(--radius)] bg-[var(--color-surface-2)] text-[var(--color-fg-subtle)]"><UploadSimpleIcon size={20} /></div>
      <div className="text-sm font-medium text-[var(--color-fg)]">Drop a file to inspect</div>
      <div className="text-xs text-[var(--color-fg-muted)]">Package JSON, IdP metadata, credentials, and executables are checked by structure.</div>
      <div className="text-xs text-[var(--color-accent)]">or click to browse</div>
      <input ref={inputRef} type="file" accept=".json,.xml,.pem,.crt,.key,.bin,.exe,.dll,.so,.dylib,.app,.appimage,.bnbloc-component" className="hidden" onChange={(event) => { const file = event.currentTarget.files?.item(0); if (file) void onFile(file); event.currentTarget.value = ""; }} />
    </div>
  );
}
