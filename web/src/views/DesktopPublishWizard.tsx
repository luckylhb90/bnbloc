import { useMemo, useRef, useState, type ChangeEvent } from "react";
import { CheckCircleIcon, UploadSimpleIcon, WarningCircleIcon } from "@phosphor-icons/react";
import { api, type UpdateConfig } from "@/lib/api";
import { gatewayBase } from "@/lib/config";
import { nextDesktopPublishStep, publishBlockReason, publishDesktopRelease, validateDesktopReleaseSelection, type DesktopPublishStep } from "@/lib/desktopRelease";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { completeUpdateConfig, describeUpdateDelta, updateCandidateForArchive, type PlatformChangeKind } from "@/views/updateConfigFields";

const STEPS = ["Signed files", "Release info", "Review", "Result"] as const;
// The local publish flow manages this computer's Apple Silicon updater.
const LOCAL_TARGET = "darwin-aarch64" as const;

const CHANGE_COPY: Record<PlatformChangeKind, string> = {
  added: "will be added",
  replaced: "will be replaced by this upload",
  removed: "will be removed — it was built for the previous version",
  kept: "kept (same version)",
  none: "not configured",
};

interface PublishResult {
  readonly version: string;
  readonly channel: string;
  readonly size: number;
  readonly sha256: string;
}

// DesktopPublishWizard publishes a locally built, signed updater archive: it
// uploads the archive, writes the Release Hub entry, then activates the
// updater under the CURRENT policy channel. It works from the last saved
// config only, so unsaved policy edits must be saved or discarded first.
export function DesktopPublishWizard({
  saved,
  dirty,
  saving,
  onSaveChanges,
  onDiscardChanges,
  onPublished,
  onPartialFailure,
}: {
  readonly saved: UpdateConfig;
  readonly dirty: boolean;
  readonly saving: boolean;
  readonly onSaveChanges: () => void;
  readonly onDiscardChanges: () => void;
  readonly onPublished: (config: UpdateConfig) => void;
  readonly onPartialFailure: (detail: string) => void;
}) {
  const toast = useToast();
  const [step, setStep] = useState<DesktopPublishStep>(0);
  const [archiveFile, setArchiveFile] = useState<File | null>(null);
  const [signatureFile, setSignatureFile] = useState<File | null>(null);
  const [version, setVersion] = useState("");
  const [notes, setNotes] = useState("");
  const [publishing, setPublishing] = useState(false);
  const [progress, setProgress] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<PublishResult | null>(null);
  // Bumped on "Publish another" so the uncontrolled file inputs remount empty.
  const [generation, setGeneration] = useState(0);
  const abortRef = useRef<AbortController | null>(null);
  const channel = saved.policy.channel;
  const channelLabel = channel === "internal" ? "Internal" : "Stable";

  const delta = useMemo(() => {
    const candidate = updateCandidateForArchive(saved, { version: version.trim(), notes, pubDate: "", target: LOCAL_TARGET, url: "(uploaded on publish)", signature: "(selected .sig)" });
    return describeUpdateDelta(saved, candidate);
  }, [saved, version, notes]);

  const selection = { archive: archiveFile, signature: signatureFile ? "selected" : null };
  const filesBlock = publishBlockReason({ ...selection, version: "1.0.0" });
  const versionBlock = publishBlockReason({ ...selection, version });

  const selectArchive = (event: ChangeEvent<HTMLInputElement>) => {
    setArchiveFile(event.currentTarget.files?.[0] ?? null);
    setError(null);
  };
  const selectSignature = (event: ChangeEvent<HTMLInputElement>) => {
    setSignatureFile(event.currentTarget.files?.[0] ?? null);
    setError(null);
  };

  const advance = async () => {
    setError(null);
    try {
      const signature = signatureFile ? await signatureFile.text() : null;
      setStep(nextDesktopPublishStep({ step, archive: archiveFile, signature, version, gatewayReady: true }));
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Check the selected updater archive and signature.");
    }
  };

  const publish = async () => {
    if (!archiveFile || !signatureFile || publishing || dirty) return;
    const abort = new AbortController();
    abortRef.current = abort;
    setPublishing(true);
    setError(null);
    setProgress("Reading the Tauri signature…");
    try {
      const signature = validateDesktopReleaseSelection({ file: archiveFile, signature: await signatureFile.text(), version, notes });
      const pubDate = new Date().toISOString();
      const trimmed = version.trim();
      const published = await publishDesktopRelease({
        archive: { file: archiveFile, signature, version: trimmed, notes, pubDate, channel },
        loadReleaseState: async () => {
          const [freshConfig, freshHub] = await Promise.all([api.getUpdateConfig(), api.getPlatformReleases()]);
          return { config: completeUpdateConfig(freshConfig), hubReleases: freshHub.releases };
        },
        upload: async (file) => {
          const uploaded = await api.uploadArtifact(file, { signal: abort.signal, onProgress: setProgress });
          return { ...uploaded, name: file.name, signature, version: trimmed, notes, pubDate };
        },
        saveHub: async (entry) => {
          await api.putRelease(entry.version, { channel: entry.channel, releaseDate: entry.releaseDate, notes: entry.notes, files: entry.files });
        },
        saveUpdateConfig: (candidate) => api.setUpdateConfig(candidate),
        onProgress: setProgress,
      });
      onPublished(completeUpdateConfig(published.updateConfig));
      setResult({ version: published.updateConfig.release.version, channel: published.hubRelease.channel, size: published.artifact.size, sha256: published.artifact.sha256 });
      setStep(3);
      toast.success(`Desktop update ${published.updateConfig.release.version} published`);
    } catch (failure) {
      const detail = failure instanceof Error ? failure.message : "Desktop release publishing failed";
      setError(detail);
      if (failure instanceof Error && "hubSaved" in failure && failure.hubSaved === true) onPartialFailure(detail);
    } finally {
      abortRef.current = null;
      setPublishing(false);
    }
  };

  const reset = () => {
    setStep(0);
    setArchiveFile(null);
    setSignatureFile(null);
    setVersion("");
    setNotes("");
    setProgress(null);
    setError(null);
    setResult(null);
    setGeneration((n) => n + 1);
  };

  return (
    <Card className="p-4">
      <div>
        <h2 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg)]">Publish from this computer</h2>
        <p className="mt-1 text-sm text-[var(--color-fg-muted)]">Four steps: select and verify the signed archive, enter release details, review every change, then publish.</p>
      </div>
      <ol className="mt-4 grid grid-cols-2 gap-2 md:grid-cols-4" aria-label="Publish progress">
        {STEPS.map((label, index) => (
          <li key={label} className={`rounded-[var(--radius)] border px-3 py-2 text-xs ${step === index ? "border-[var(--color-accent)] text-[var(--color-fg)]" : "border-[var(--color-border)] text-[var(--color-fg-subtle)]"}`} aria-current={step === index ? "step" : undefined}>{index + 1}. {label}</li>
        ))}
      </ol>

      <div hidden={step !== 0} className="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2">
        <p className="md:col-span-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3 text-xs text-[var(--color-fg-muted)]">
          Select the paired build outputs from <code className="font-mono text-[var(--color-fg)]">apps/desktop/src-tauri/target/release/bundle/macos/</code>: <code className="font-mono text-[var(--color-fg)]">bnbloc.app.tar.gz</code> and <code className="font-mono text-[var(--color-fg)]">bnbloc.app.tar.gz.sig</code>.
        </p>
        <Field label="Updater archive (.app.tar.gz)">
          <Input key={`archive-${generation}`} type="file" accept=".app.tar.gz,application/gzip" disabled={publishing} onChange={selectArchive} />
          {archiveFile && <span className="text-xs text-[var(--color-fg-muted)]">{archiveFile.name} · {archiveFile.size.toLocaleString()} bytes</span>}
        </Field>
        <Field label="Paired updater signature (.sig)">
          <Input key={`signature-${generation}`} type="file" accept=".sig,text/plain" disabled={publishing} onChange={selectSignature} />
          {signatureFile && <span className="text-xs text-[var(--color-fg-muted)]">{signatureFile.name}</span>}
        </Field>
      </div>
      {step === 0 && filesBlock && <p className="mt-3 text-xs text-[var(--color-fg-muted)]" role="status">{filesBlock}</p>}

      <div hidden={step !== 1} className="mt-4 grid gap-4 md:grid-cols-2">
        <Field label="Release version" hint="Use the version from the signed desktop build, for example 1.2.3 (optionally with a prerelease suffix).">
          <Input value={version} placeholder="1.2.3" aria-invalid={!!version && !!versionBlock} onChange={(event) => setVersion(event.target.value)} />
        </Field>
        <div className="rounded-[var(--radius)] border border-[var(--color-border)] p-3 text-sm">
          Channel: <b>{channelLabel}</b> (from the saved update policy)<br />Auto-install: <b>{saved.policy.autoInstall ? "On" : "Off"}</b>
        </div>
        <div className="md:col-span-2">
          <Field label="Release notes"><Textarea rows={4} value={notes} onChange={(event) => setNotes(event.target.value)} /></Field>
        </div>
      </div>

      <div hidden={step !== 2} className="mt-4 space-y-3 rounded-[var(--radius)] border border-[var(--color-border)] p-4 text-sm">
        <p>Version: <b>{delta.version.from || "(none)"}</b> → <b>{delta.version.to}</b> · Channel: <b>{channelLabel}</b></p>
        <p>Gateway: <b>{gatewayBase() || window.location.origin}</b></p>
        <p>Archive: <b>{archiveFile?.name}</b> · {archiveFile?.size.toLocaleString()} bytes · Signature: <b>{signatureFile?.name}</b></p>
        <div>
          <p className="font-medium">Update policy changes</p>
          {delta.policy.length === 0 ? (
            <p className="text-[var(--color-fg-muted)]">None — the saved policy is kept as is.</p>
          ) : (
            <ul className="list-disc pl-5">
              {delta.policy.map((change) => <li key={change.field}>{change.label}: {change.from} → <b>{change.to}</b></li>)}
            </ul>
          )}
        </div>
        <div>
          <p className="font-medium">Platform artifacts</p>
          <ul className="list-disc pl-5">
            {delta.platforms.map((p) => <li key={p.target}><span className="tabular">{p.target}</span>: {CHANGE_COPY[p.change]}</li>)}
          </ul>
        </div>
        <p className="text-[var(--color-fg-muted)]">The updater activates only after the Release Hub entry saves.</p>
      </div>

      {step === 3 && result && (
        <div className="mt-4 flex flex-col gap-3 rounded-[var(--radius)] border border-[var(--color-accent)]/40 p-4 text-sm" role="status">
          <p className="flex items-center gap-2 font-medium text-[var(--color-fg)]"><CheckCircleIcon size={18} weight="fill" className="text-[var(--color-accent)]" /> Published v{result.version} on the {result.channel} channel</p>
          <p className="break-all text-[var(--color-fg-muted)]">{result.size.toLocaleString()} bytes · SHA-256 {result.sha256}</p>
          <p className="text-[var(--color-fg-muted)]">Existing clients discover the update on their next check.</p>
          <div><Button variant="outline" onClick={reset}>Publish another</Button></div>
        </div>
      )}

      {dirty && step !== 3 && (
        <div className="mt-4 rounded-[var(--radius)] border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 p-3 text-sm text-[var(--color-fg-muted)]" role="status">
          <p className="flex items-center gap-1.5 font-medium text-[var(--color-fg)]"><WarningCircleIcon size={16} weight="fill" className="text-[var(--color-warning)]" /> Unsaved policy changes</p>
          <p className="mt-1">Publishing works from the saved update policy. Save or discard your policy edits before publishing.</p>
          <div className="mt-2 flex gap-2">
            <Button size="sm" variant="primary" disabled={saving || publishing} onClick={onSaveChanges}>{saving ? "Saving…" : "Save changes"}</Button>
            <Button size="sm" variant="ghost" disabled={saving || publishing} onClick={onDiscardChanges}>Discard changes</Button>
          </div>
        </div>
      )}
      {progress && step !== 3 && <p className="mt-3 text-xs text-[var(--color-fg-muted)]" aria-live="polite">{progress}</p>}
      {error && <div className="mt-3"><ErrorBox>{error}</ErrorBox></div>}

      {step !== 3 && (
        <div className="mt-4 flex flex-wrap justify-between gap-2">
          <Button variant="ghost" disabled={publishing || step === 0} onClick={() => { setError(null); setStep((step === 2 ? 1 : 0) as DesktopPublishStep); }}>← Back</Button>
          <div className="flex gap-2">
            {publishing && <Button variant="ghost" onClick={() => abortRef.current?.abort()}>Cancel upload</Button>}
            {step > 0 && <Button variant="ghost" disabled={publishing} onClick={reset}>Start over</Button>}
            {step < 2 && <Button variant="primary" disabled={publishing || Boolean(step === 1 ? versionBlock : filesBlock)} onClick={() => void advance()}>Continue →</Button>}
            {step === 2 && <Button variant="primary" disabled={publishing || dirty || Boolean(versionBlock)} onClick={() => void publish()}><UploadSimpleIcon size={15} />{publishing ? "Publishing…" : "Publish update"}</Button>}
          </div>
        </div>
      )}
    </Card>
  );
}
