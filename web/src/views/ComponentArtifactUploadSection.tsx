import { useEffect, useRef } from "react";
import { CheckCircleIcon, UploadSimpleIcon, WarningCircleIcon, XCircleIcon } from "@phosphor-icons/react";
import type { Dispatch, SetStateAction } from "react";
import type { ArtifactRequirement } from "@/lib/componentArtifactRequirements";
import { api } from "@/lib/api";
import type { ArtifactUploadResult } from "@/lib/artifactUpload";
import { cn } from "@/lib/cn";
import { formatBytes } from "@/lib/format";
import { Badge } from "@/ui/Card";
import { Input } from "@/ui/Input";
import { errorText } from "@/lib/errors";

export type ArtifactUploadState = {
  readonly fileName?: string;
  readonly fileSize?: number;
  readonly progress?: number;
  readonly uploaded?: ArtifactUploadResult;
  readonly error?: string;
};

export type ArtifactUploadStates = Readonly<Record<string, ArtifactUploadState | undefined>>;

// uploadInto streams one platform binary through the shared artifact upload and
// mirrors its progress/result/error into the wizard's per-platform state.
async function uploadInto(platform: string, file: File, setUploads: Dispatch<SetStateAction<ArtifactUploadStates>>): Promise<void> {
  const base = { fileName: file.name, fileSize: file.size };
  setUploads((current) => ({ ...current, [platform]: { ...base, progress: 0 } }));
  try {
    const uploaded = await api.uploadArtifact(file, {
      onBytes: (progress) =>
        setUploads((current) => ({
          ...current,
          [platform]: { ...base, progress: progress.total ? Math.round((progress.loaded / progress.total) * 100) : undefined },
        })),
    });
    setUploads((current) => ({ ...current, [platform]: { ...base, progress: 100, uploaded } }));
  } catch (caught: unknown) {
    const error = errorText(caught, "artifact upload failed");
    setUploads((current) => ({ ...current, [platform]: { ...base, error } }));
  }
}

export function ComponentArtifactUploadSection({ requirements, uploads, setUploads, disabled, initialFiles }: {
  readonly requirements: readonly ArtifactRequirement[];
  readonly uploads: ArtifactUploadStates;
  readonly setUploads: Dispatch<SetStateAction<ArtifactUploadStates>>;
  readonly disabled: boolean;
  readonly initialFiles?: Readonly<Record<string, File>>;
}) {
  const attempted = useRef(new Set<string>());
  useEffect(() => {
    for (const requirement of requirements) {
      const file = initialFiles?.[requirement.platform];
      if (!file || uploads[requirement.platform]?.uploaded || attempted.current.has(requirement.platform)) continue;
      attempted.current.add(requirement.platform);
      void uploadInto(requirement.platform, file, setUploads);
    }
  }, [initialFiles, requirements, setUploads, uploads]);

  if (requirements.length === 0) return null;
  const complete = requirements.filter((requirement) => uploads[requirement.platform]?.uploaded).length;
  return (
    <section className="rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h4 className="text-sm font-semibold text-[var(--color-fg)]">Platform artifacts</h4>
          <p className="mt-1 text-xs leading-relaxed text-[var(--color-fg-muted)]">
            Upload the real binary for each declared platform. The gateway returns the content URL and sha256 used in the package sent to publish.
          </p>
        </div>
        <Badge tone={complete === requirements.length ? "accent" : "warning"}>{complete}/{requirements.length} uploaded</Badge>
      </div>
      <div className="mt-3 grid gap-2">
        {requirements.map((requirement) => (
          <ArtifactUploadRow
            key={requirement.platform}
            platform={requirement.platform}
            state={uploads[requirement.platform]}
            disabled={disabled}
            setUploads={setUploads}
            onRetry={() => {
              const file = initialFiles?.[requirement.platform];
              if (!file) return;
              attempted.current.delete(requirement.platform);
              setUploads((current) => ({ ...current, [requirement.platform]: undefined }));
            }}
          />
        ))}
      </div>
    </section>
  );
}

function ArtifactUploadRow({
  platform,
  state,
  disabled,
  setUploads,
  onRetry,
}: {
  readonly platform: string;
  readonly state?: ArtifactUploadState;
  readonly disabled: boolean;
  readonly setUploads: Dispatch<SetStateAction<ArtifactUploadStates>>;
  readonly onRetry: () => void;
}) {
  const uploaded = state?.uploaded;
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="tabular text-sm font-semibold text-[var(--color-fg)]">{platform}</span>
            <ArtifactStatusBadge state={state} />
          </div>
          <p className="mt-1 break-all text-xs text-[var(--color-fg-subtle)]">
            {uploaded ? `sha256 ${uploaded.sha256}` : state?.fileName ? `${state.fileName} · ${formatBytes(state.fileSize ?? 0)}` : "No binary uploaded yet."}
          </p>
        </div>
        <label className={cn("relative inline-flex shrink-0", disabled && "pointer-events-none opacity-60")}>
          <Input
            type="file"
            className="absolute inset-0 opacity-0"
            disabled={disabled}
            aria-label={`Upload ${platform} artifact`}
            onChange={(event) => {
              const file = event.currentTarget.files?.item(0);
              if (file) void uploadInto(platform, file, setUploads);
              event.currentTarget.value = "";
            }}
          />
          <span
            className={cn(
              "tactile inline-flex h-9 items-center gap-2 rounded-[var(--radius)] px-4 text-sm",
              "border border-[var(--color-border-strong)] text-[var(--color-fg)]",
              uploaded ? "bg-transparent" : "bg-[var(--color-elevated)]",
            )}
          >
            <UploadSimpleIcon size={15} /> {uploaded ? "Replace" : "Upload binary"}
          </span>
        </label>
      </div>
      {state?.progress !== undefined && !uploaded && !state.error && (
        <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-[var(--color-border)]">
          <div className="h-full origin-left rounded-full bg-[var(--color-accent)] transition-transform" style={{ transform: `scaleX(${state.progress / 100})` }} />
        </div>
      )}
      {state?.error && <div className="mt-2 flex items-center justify-between gap-2 text-xs"><p className="text-[var(--color-danger)]">{state.error}</p><button className="font-medium text-[var(--color-accent)] underline" onClick={onRetry} disabled={disabled}>Choose this binary again</button></div>}
    </div>
  );
}

function ArtifactStatusBadge({ state }: { readonly state?: ArtifactUploadState }) {
  if (state?.uploaded) {
    return <Badge tone="accent"><CheckCircleIcon size={12} weight="fill" /> Uploaded</Badge>;
  }
  if (state?.error) {
    return <Badge tone="danger"><XCircleIcon size={12} weight="fill" /> Failed</Badge>;
  }
  if (state?.fileName) {
    return <Badge tone="neutral"><UploadSimpleIcon size={12} /> Uploading</Badge>;
  }
  return <Badge tone="warning"><WarningCircleIcon size={12} weight="fill" /> Required</Badge>;
}
