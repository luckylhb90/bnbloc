import type { Dispatch, SetStateAction } from "react";
import { ArrowLeftIcon, ArrowRightIcon } from "@phosphor-icons/react";
import type { ArtifactRequirement } from "@/lib/componentArtifactRequirements";
import { uploadedArtifactsComplete } from "@/lib/componentArtifactRequirements";
import { Button } from "@/ui/Button";
import { ComponentArtifactUploadSection, type ArtifactUploadStates } from "@/views/ComponentArtifactUploadSection";

export function ComponentArtifactWizardStep({ requirements, uploads, setUploads, initialFiles, error, onBack, onContinue }: {
  readonly requirements: readonly ArtifactRequirement[];
  readonly uploads: ArtifactUploadStates;
  readonly setUploads: Dispatch<SetStateAction<ArtifactUploadStates>>;
  readonly initialFiles?: Readonly<Record<string, File>>;
  readonly error: string | null;
  readonly onBack: () => void;
  readonly onContinue: () => void;
}) {
  const ready = uploadedArtifactsComplete(requirements, uploads);
  return <section className="grid gap-4" aria-label="Upload binaries">
    <div><p className="text-xs font-medium uppercase tracking-wide text-[var(--color-accent)]">Step 3 of 4 · Binaries</p><h4 className="mt-1 text-base font-semibold text-[var(--color-fg)]">Upload the platform binaries</h4><p className="mt-1 text-sm text-[var(--color-fg-muted)]">Upload each selected platform build. Your binary from the first step uploads here automatically.</p></div>
    <ComponentArtifactUploadSection requirements={requirements} uploads={uploads} setUploads={setUploads} disabled={false} initialFiles={initialFiles} />
    <p className="text-xs text-[var(--color-fg-subtle)]">{requirements.length} platform binary{requirements.length === 1 ? "" : "ies"} required.</p>
    {error && <div role="alert" className="text-sm text-[var(--color-danger)]">{error}</div>}
    <div className="flex justify-between border-t border-[var(--color-border)] pt-4"><Button variant="ghost" onClick={onBack}><ArrowLeftIcon size={15} /> Back</Button><Button variant="primary" disabled={!ready} onClick={onContinue}>Continue <ArrowRightIcon size={15} /></Button></div>
  </section>;
}
