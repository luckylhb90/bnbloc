import { useEffect, useMemo, useRef, useState } from "react";
import { CheckCircleIcon } from "@phosphor-icons/react";
import { api, ApiError, type ComponentRollout, type ComponentValidationStep, type ComponentValidationStepId } from "@/lib/api";
import { artifactRequirementsForPackage, buildFinalComponentPackageJSON, uploadedArtifactsComplete, type ArtifactRequirement } from "@/lib/componentArtifactRequirements";
import { inspectComponentFile, type InspectedComponentFile } from "@/lib/componentPackageIntake";
import { createPackageDraft, packageDraftFromEnvelope, nextPublishWizardStep, previousPublishWizardStep, type PackageDraftFields, type PublishWizardStep } from "@/lib/componentPublishWizard";
import type { ComponentPackageEnvelope } from "@/lib/apiTypes";
import { formatBytes } from "@/lib/format";
import { Button } from "@/ui/Button";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";
import type { ArtifactUploadStates } from "@/views/ComponentArtifactUploadSection";
import { ComponentUploadCard } from "@/views/ComponentUploadDropZone";
import { ComponentArtifactWizardStep } from "@/views/ComponentArtifactWizardStep";
import { ComponentPackageBuilderForm } from "@/views/ComponentPackageBuilderForm";
import { ComponentPublishSource, type PublishSourceMode } from "@/views/ComponentPublishSource";
import { ComponentUploadReviewPanel } from "@/views/ComponentUploadReviewPanel";
import type { ReviewPackage } from "@/views/componentPublishTypes";
import { ComponentValidationTimeline } from "@/views/ComponentValidationTimeline";

const LABEL: Record<ComponentValidationStepId, string> = { reading: "Inspect input", schema: "Package contract", signature: "Signature", artifact: "Artifact validation", probe: "Conformance probe", review: "Review", publishing: "Publish" };
const INITIAL: readonly ComponentValidationStep[] = [
  { id: "reading", label: LABEL.reading, state: "pending", detail: "Waiting for a file." },
  { id: "schema", label: LABEL.schema, state: "pending", detail: "Package inspection has not started." },
  { id: "signature", label: LABEL.signature, state: "pending", detail: "Server verification runs at publish." },
  { id: "artifact", label: LABEL.artifact, state: "pending", detail: "Artifact metadata will be checked before the server probe." },
  { id: "probe", label: LABEL.probe, state: "pending", detail: "The gateway installer runs the kind-specific probe." },
  { id: "review", label: LABEL.review, state: "pending", detail: "Review is available after local checks pass." },
  { id: "publishing", label: LABEL.publishing, state: "pending", detail: "No record is persisted before required checks pass." },
];
type Phase = "idle" | "inspecting" | "publishing" | "success";
type Published = { readonly name: string; readonly version: string; readonly rollout: ComponentRollout; readonly keyId: string | null };
type SourceChoice = { readonly envelope?: ComponentPackageEnvelope; readonly draft: PackageDraftFields; readonly fileName: string; readonly fileSize: number; readonly initialFile?: File; readonly initialPlatform?: string; readonly mode?: PublishSourceMode };

export function ComponentUploadFlow({ onPublished }: { readonly onPublished?: (name: string, secret?: string) => Promise<void> | void }) {
  const toast = useToast();
  const [phase, setPhase] = useState<Phase>("idle");
  const [step, setStep] = useState<PublishWizardStep>("source");
  const [choice, setChoice] = useState<SourceChoice | null>(null);
  const [review, setReview] = useState<ReviewPackage | null>(null);
  const [inspection, setInspection] = useState<InspectedComponentFile | null>(null);
  const [pendingBinary, setPendingBinary] = useState<File | null>(null);
  const [signature, setSignature] = useState("");
  const [rollout, setRollout] = useState<ComponentRollout>("auto");
  const [requirements, setRequirements] = useState<readonly ArtifactRequirement[]>([]);
  const [uploads, setUploads] = useState<ArtifactUploadStates>({});
  const [checks, setChecks] = useState<readonly ComponentValidationStep[]>(INITIAL);
  const [error, setError] = useState<string | null>(null);
  const [hasArtifacts, setHasArtifacts] = useState(false);
  const [showChecks, setShowChecks] = useState(false);
  const artifactsReady = uploadedArtifactsComplete(requirements, uploads);
  const finalJSON = useMemo(() => review && artifactsReady ? buildFinalComponentPackageJSON(review.envelope, uploads) : "", [artifactsReady, review, uploads]);
  const priorJSON = useRef("");
  const [published, setPublished] = useState<Published | null>(null);
  // Whether this gateway refuses unsigned packages. Only a dev gateway that
  // explicitly allows unsigned installs reports false; unknown means required.
  const [signatureRequired, setSignatureRequired] = useState(true);
  useEffect(() => {
    let cancelled = false;
    api.listComponentKeys().then(
      (res) => { if (!cancelled) setSignatureRequired(res.signatureRequired !== false); },
      () => { if (!cancelled) setSignatureRequired(true); },
    );
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    if (step !== "artifacts" || requirements.length === 0) return;
    if (artifactsReady) { setError(null); setChecks((current) => withCheck(current, "artifact", "done", "Every selected binary has a Gateway URL and SHA-256.")); return; }
    const failed = requirements.find((item) => uploads[item.platform]?.error);
    if (failed) { setChecks((current) => withCheck(current, "artifact", "failed", `${failed.platform}: ${uploads[failed.platform]?.error ?? "artifact upload failed"}`)); return; }
    setChecks((current) => withCheck(current, "artifact", "running", missingArtifacts(requirements, uploads)));
  }, [artifactsReady, requirements, step, uploads]);
  useEffect(() => { if (priorJSON.current && priorJSON.current !== finalJSON) setSignature(""); priorJSON.current = finalJSON; }, [finalJSON]);

  const setPackage = (envelope: ComponentPackageEnvelope, fileName: string, fileSize: number) => {
    setChoice({ envelope, draft: packageDraftFromEnvelope(envelope), fileName, fileSize }); setReview(null); setStep("configure"); setPhase("idle"); setInspection({ type: "package", envelope, nextStep: "configure" }); setPendingBinary(null); setError(null); setUploads({}); setSignature("");
    setChecks(withCheck(withCheck(INITIAL, "reading", "done", `${fileName} · ${formatBytes(fileSize)}`), "schema", "done", `${envelope.kind} · ${envelope.name} v${envelope.version}`));
  };

  const inspectFile = async (file: File) => {
    setPhase("inspecting"); setError(null); setInspection(null); setPendingBinary(null);
    try {
      const result = await inspectComponentFile(file);
      setInspection(result); setPhase("idle");
      if (result.type === "package") { setPackage(result.envelope, file.name, file.size); return; }
      if (result.type === "binary") { setPendingBinary(file); return; }
      setError(result.message);
    } catch (cause: unknown) {
      const message = cause instanceof Error ? cause.message : "We could not inspect the file structure.";
      setPhase("idle"); setError(message); setInspection({ type: "unsupported-file", stage: "source", message, recovery: "choose-json-or-binary" });
    }
  };

  const startBinary = (file: File, mode: PublishSourceMode, platform?: string) => {
    setChoice({ draft: { ...createPackageDraft(mode.kind, mode.runtime), ...(platform ? { platformArtifacts: [platform] } : {}) }, fileName: file.name, fileSize: file.size, initialFile: file, ...(platform ? { initialPlatform: platform } : {}), mode });
    setInspection(null); setPendingBinary(null); setReview(null); setStep("configure"); setPhase("idle"); setError(null); setUploads({}); setSignature("");
    setChecks(withCheck(INITIAL, "reading", "done", `${file.name} · ${formatBytes(file.size)}`));
  };
  const startManual = (mode: PublishSourceMode, prior?: InspectedComponentFile) => {
    setChoice({ draft: createPackageDraft(mode.kind, mode.runtime), fileName: "New package", fileSize: 0, mode }); setInspection(prior ?? null); setPendingBinary(null); setReview(null); setStep("configure"); setPhase("idle"); setError(null); setUploads({}); setSignature("");
  };

  const configureDone = (envelope: ComponentPackageEnvelope, selectedRollout: ComponentRollout) => {
    const needed = artifactRequirementsForPackage(envelope);
    setReview({ envelope, fileName: choice?.fileName ?? "Package", fileSize: choice?.fileSize ?? 0 }); setRollout(selectedRollout); setRequirements(needed); setUploads((current) => Object.fromEntries(needed.flatMap((item) => current[item.platform]?.uploaded ? [[item.platform, current[item.platform]]] : [])));
    setSignature(""); setHasArtifacts(needed.length > 0); setError(null);
    setChecks((current) => withCheck(withCheck(current, "schema", "done", `${envelope.kind} · ${envelope.name} v${envelope.version}`), "artifact", needed.length ? "running" : "done", needed.length ? artifactDetail(needed) : "No binary upload is needed."));
    setStep(nextPublishWizardStep("configure", { runtime: envelope.runtime }) ?? "publish");
  };
  const goBack = () => { if (phase === "publishing") return; const previous = previousPublishWizardStep(step, review?.envelope.runtime ?? choice?.draft.runtime); if (previous) setStep(previous); };
  const nextAfterArtifacts = () => { const next = nextPublishWizardStep("artifacts", { artifactsReady }); if (!next) { setError(missingArtifacts(requirements, uploads)); return; } setError(null); setStep(next); };
  const publish = async () => {
    if (!review || phase === "publishing") return;
    if (!artifactsReady) { setError(missingArtifacts(requirements, uploads)); setStep("artifacts"); return; }
    if (signatureRequired && !signature.trim()) { setError("This gateway only installs signed packages. Attach the detached signature for the package JSON above."); return; }
    const running = serverRunning(checks); setPhase("publishing"); setError(null); setChecks(running);
    try { const saved = await api.publishComponent({ package: finalJSON, signature, rollout }); setPhase("success"); setChecks(serverDone(running, saved.trust.keyId ?? "unsigned dev package", saved.rollout)); setPublished({ name: saved.name, version: saved.version, rollout: saved.rollout, keyId: saved.trust.keyId ?? null }); toast.success(`Published ${saved.name} ${saved.version}`); await onPublished?.(saved.name, saved.secret); }
    catch (cause: unknown) { const message = cause instanceof ApiError ? cause.message : cause instanceof Error ? cause.message : "Publish failed."; setPhase("idle"); setStep("publish"); setError(message); setChecks(withCheck(running, failingCheck(message), "failed", message)); }
  };
  const reset = () => { setChoice(null); setReview(null); setInspection(null); setPendingBinary(null); setStep("source"); setPhase("idle"); setSignature(""); setRequirements([]); setUploads({}); setHasArtifacts(false); setError(null); setChecks(INITIAL); setPublished(null); };

  return <ComponentUploadCard>
    <WizardProgress current={step} hasArtifacts={hasArtifacts} />
    <div aria-live="polite" aria-busy={phase === "inspecting" || phase === "publishing"}>
      {published ? <PublishSuccess published={published} checks={checks} onAnother={reset} /> : <>
      {step === "source" && <><ComponentPublishSource onFile={(file) => void inspectFile(file)} onManual={startManual} inspection={inspection} pendingBinary={pendingBinary} onBinaryContract={startBinary} />{phase === "inspecting" && <p className="text-sm text-[var(--color-fg-muted)]">Inspecting the file structure…</p>}{error && !inspection && <ErrorBox>{error}</ErrorBox>}{showChecks && <ComponentValidationTimeline steps={checks} />}{checks.some((check) => check.state === "failed") && !showChecks && <button className="text-xs underline" onClick={() => setShowChecks(true)}>Show validation details</button>}</>}
      {choice && <div hidden={step !== "configure"}><ComponentPackageBuilderForm key={`${choice.fileName}:${choice.draft.kind}:${choice.draft.runtime}`} initialDraft={choice.draft} initialEnvelope={choice.envelope} onBack={goBack} onContinue={configureDone} /></div>}
      {step === "artifacts" && review && <ComponentArtifactWizardStep requirements={requirements} uploads={uploads} setUploads={setUploads} initialFiles={choice?.initialFile && choice.initialPlatform ? { [choice.initialPlatform]: choice.initialFile } : choice?.initialFile && requirements.length === 1 ? { [requirements[0]!.platform]: choice.initialFile } : undefined} error={error} onBack={goBack} onContinue={nextAfterArtifacts} />}
      {step === "publish" && review && <>{error && <ErrorBox>{error}</ErrorBox>}<ComponentUploadReviewPanel review={review} signature={signature} setSignature={setSignature} signatureRequired={signatureRequired} rollout={rollout} setRollout={setRollout} artifactRequirements={requirements} artifactUploads={uploads} finalPackageJSON={finalJSON} publishing={phase === "publishing"} publish={publish} onBack={goBack} discard={reset} /><ComponentValidationTimeline steps={checks} /></>}
      </>}
    </div>
  </ComponentUploadCard>;
}

function WizardProgress({ current, hasArtifacts }: { readonly current: PublishWizardStep; readonly hasArtifacts: boolean }) { const steps: readonly { readonly id: PublishWizardStep; readonly label: string }[] = [{ id: "source", label: "Start" }, { id: "configure", label: "Configure" }, { id: "artifacts", label: hasArtifacts ? "Upload binaries" : "No binaries" }, { id: "publish", label: "Publish" }]; return <nav aria-label="Publish progress" className="mb-4 flex items-center gap-2 border-b border-[var(--color-border)] pb-3">{steps.map((item, index) => <div key={item.id} className="flex min-w-0 items-center gap-2"><span className={item.id === current ? "grid size-6 place-items-center rounded-full bg-[var(--color-accent)] text-xs font-medium text-[var(--color-accent-fg)]" : "grid size-6 place-items-center rounded-full border border-[var(--color-border)] text-xs text-[var(--color-fg-subtle)]"}>{index + 1}</span><span className={item.id === current ? "text-xs font-medium text-[var(--color-fg)]" : "text-xs text-[var(--color-fg-subtle)]"}>{item.label}</span>{index < steps.length - 1 && <span className="mx-1 h-px w-5 bg-[var(--color-border)]" />}</div>)}</nav>; }
function withCheck(checks: readonly ComponentValidationStep[], id: ComponentValidationStepId, state: ComponentValidationStep["state"], detail: string): readonly ComponentValidationStep[] { return checks.map((item) => item.id === id ? { ...item, state, detail } : item); }
function serverRunning(checks: readonly ComponentValidationStep[]): readonly ComponentValidationStep[] { return checks.map((item) => item.id === "signature" || item.id === "artifact" ? { ...item, state: "running", detail: item.id === "signature" ? "Verifying detached signature against trusted publisher keys." : "Checking artifact metadata and hashes before activation." } : item.id === "probe" || item.id === "review" || item.id === "publishing" ? { ...item, state: "pending", detail: "Waiting for required server checks." } : item); }
function serverDone(checks: readonly ComponentValidationStep[], key: string, rollout: ComponentRollout): readonly ComponentValidationStep[] { const detail: Partial<Record<ComponentValidationStepId, string>> = { signature: `Gateway accepted publisher key ${key}.`, artifact: "Gateway accepted artifact metadata and hashes.", probe: "Kind-specific conformance probe passed.", review: "Published from reviewed package facts.", publishing: `Component record persisted with ${rollout} rollout.` }; return checks.map((item) => detail[item.id] ? { ...item, state: "done", detail: detail[item.id]! } : item); }
function failingCheck(message: string): ComponentValidationStepId { return /signature|publisher key/i.test(message) ? "signature" : /artifact|sha256/i.test(message) ? "artifact" : /probe|conformance/i.test(message) ? "probe" : "publishing"; }
function artifactDetail(items: readonly ArtifactRequirement[]): string { return `${items.length} platform binary${items.length === 1 ? "" : "ies"} required before publishing.`; }
function missingArtifacts(items: readonly ArtifactRequirement[], uploads: ArtifactUploadStates): string { const missing = items.filter((item) => !uploads[item.platform]?.uploaded).map((item) => item.platform); return missing.length ? `Upload a binary for each required platform: ${missing.join(", ")}.` : "Upload every required platform binary before continuing."; }
function PublishSuccess({ published, checks, onAnother }: { readonly published: Published; readonly checks: readonly ComponentValidationStep[]; readonly onAnother: () => void }) {
  return <section className="grid gap-3" aria-label="Component published">
    <p className="flex items-center gap-2 text-base font-semibold text-[var(--color-fg)]"><CheckCircleIcon size={20} weight="fill" className="text-[var(--color-accent)]" /> Published {published.name} v{published.version}</p>
    <p className="text-sm text-[var(--color-fg-muted)]">Rollout: <b>{published.rollout}</b> · {published.keyId ? <>Signed by publisher key <span className="font-mono">{published.keyId}</span></> : "Unsigned dev package"}</p>
    <ComponentValidationTimeline steps={checks} />
    <div><Button variant="outline" onClick={onAnother}>Publish another</Button></div>
  </section>;
}
