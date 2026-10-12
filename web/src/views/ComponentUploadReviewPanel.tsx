import { ArrowLeftIcon, DownloadSimpleIcon, PlusCircleIcon, XCircleIcon } from "@phosphor-icons/react";
import type { ComponentPackageEnvelope, ComponentRollout } from "@/lib/api";
import { saveBlob } from "@/lib/saveBlob";
import { uploadedArtifactsComplete, type ArtifactRequirement } from "@/lib/componentArtifactRequirements";
import { Badge } from "@/ui/Card";
import { Button } from "@/ui/Button";
import { Field, Input, Textarea } from "@/ui/Input";
import type { ArtifactUploadStates } from "@/views/ComponentArtifactUploadSection";
import { ComponentRolloutOptions } from "@/views/ComponentRolloutOptions";
import type { ReviewPackage } from "@/views/componentPublishTypes";

export function ComponentUploadReviewPanel(props: {
  readonly review: ReviewPackage;
  readonly signature: string;
  readonly setSignature: (signature: string) => void;
  readonly signatureRequired: boolean;
  readonly rollout: ComponentRollout;
  readonly setRollout: (rollout: ComponentRollout) => void;
  readonly artifactRequirements: readonly ArtifactRequirement[];
  readonly artifactUploads: ArtifactUploadStates;
  readonly finalPackageJSON: string;
  readonly publishing: boolean;
  readonly publish: () => Promise<void>;
  readonly onBack: () => void;
  readonly discard: () => void;
}) {
  const envelope = props.review.envelope;
  const artifactsReady = uploadedArtifactsComplete(props.artifactRequirements, props.artifactUploads);
  const fileName = packageFileName(envelope);
  const missingSignature = props.signatureRequired && !props.signature.trim();
  return (
    <section className="grid gap-4" aria-label="Review and publish component">
      <div>
        <p className="text-xs font-medium uppercase tracking-wide text-[var(--color-accent)]">Step 4 of 4 · Sign and publish</p>
        <h4 className="mt-1 text-base font-semibold text-[var(--color-fg)]">Review before publishing</h4>
        <p className="mt-1 text-sm text-[var(--color-fg-muted)]">Check the summary, sign these exact package bytes, then publish.</p>
      </div>
      <div className="flex flex-wrap items-center gap-2"><h3 className="text-base font-semibold text-[var(--color-fg)]">{envelope.name}</h3><Badge tone="neutral" className="tabular">v{envelope.version}</Badge><Badge tone="accent">{kindName(envelope.kind)}</Badge><Badge tone="neutral">{envelope.runtime}</Badge></div>
      <p className="text-sm text-[var(--color-fg-muted)]">{envelope.description || "No description provided."}</p>
      <div className="grid gap-2 text-xs sm:grid-cols-2"><Fact label="Publisher" value={envelope.publisher} /><Fact label="Contract" value={contractFact(envelope)} /><Fact label="Artifacts" value={artifactDetail(envelope)} /><Fact label="Signature" value={props.signature.trim() ? "Detached signature attached" : !artifactsReady ? "Upload artifacts first" : props.signatureRequired ? "Required — not attached yet" : "Not signed (allowed on this gateway)"} /></div>
      <Field label="Rollout policy"><ComponentRolloutOptions value={props.rollout} onChange={props.setRollout} disabled={props.publishing} /></Field>
      <Field label="Final package JSON to sign" hint="Sign these exact bytes before publishing."><Textarea rows={5} value={props.finalPackageJSON} readOnly className="font-mono text-xs" spellCheck={false} /></Field>
      <div className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3 text-xs text-[var(--color-fg-muted)]">
        <div><Button variant="outline" size="sm" disabled={!props.finalPackageJSON} onClick={() => downloadText(props.finalPackageJSON, fileName)}><DownloadSimpleIcon size={14} /> Download package JSON</Button></div>
        <p>Sign the downloaded file with your trusted publisher key (minisign, legacy Ed25519 format), then attach the resulting <span className="font-mono">.minisig</span> below:</p>
        <code className="block break-all font-mono text-[var(--color-fg)]">minisign -S -l -s &lt;publisher.key&gt; -m {fileName}</code>
        <p>Or publish from a terminal: <code className="break-all font-mono text-[var(--color-fg)]">bnbloc cores install --package {fileName} --signature {fileName}.minisig</code></p>
      </div>
      <Field label="Detached signature" hint={props.signatureRequired ? "Required: this gateway only installs signed packages. Upload the .minisig file or paste its contents." : "Optional on this development gateway (unsigned installs are allowed)."}>
        <div className="flex flex-col gap-2"><Input type="file" accept=".minisig,.sig,text/plain" disabled={!artifactsReady || props.publishing} aria-label="Upload detached signature file" onChange={(event) => { const file = event.currentTarget.files?.item(0); if (file) void file.text().then(props.setSignature); event.currentTarget.value = ""; }} /><Textarea rows={3} value={props.signature} onChange={(event) => props.setSignature(event.target.value)} placeholder="Paste detached signature" className="font-mono text-xs" spellCheck={false} disabled={!artifactsReady || props.publishing} /></div>
      </Field>
      {missingSignature && artifactsReady && <p className="text-xs text-[var(--color-fg-muted)]" role="status">Attach the signature for the package JSON above to enable publishing.</p>}
      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-[var(--color-border)] pt-4"><div className="flex items-center gap-2"><Button variant="ghost" onClick={props.onBack} disabled={props.publishing}><ArrowLeftIcon size={15} /> Back</Button><Button variant="ghost" onClick={props.discard} disabled={props.publishing}><XCircleIcon size={15} /> Start over</Button></div><Button variant="primary" onClick={() => void props.publish()} disabled={props.publishing || !artifactsReady || missingSignature}>{props.publishing ? "Publishing…" : "Publish"}<PlusCircleIcon size={16} weight="fill" /></Button></div>
    </section>
  );
}

function kindName(kind: ComponentPackageEnvelope["kind"]): string { return kind === "bnbloc.core.package/v1" ? "Core" : kind === "bnbloc.plugin.package/v1" ? "Plugin" : kind === "bnbloc.notifier.package/v1" ? "Notifier" : kind === "bnbloc.mcp.package/v1" ? "MCP tool" : "Auth provider"; }
function contractFact(envelope: ComponentPackageEnvelope): string { return envelope.contract ?? (envelope.protocol ? `Core Protocol ${envelope.protocol.min}–${envelope.protocol.max}` : "No contract declared"); }
function artifactDetail(envelope: ComponentPackageEnvelope): string { const count = Object.keys(envelope.artifacts ?? {}).length; return envelope.runtime === "sidecar" || envelope.runtime === "stdio" ? `${count} platform binary${count === 1 ? "" : "ies"} required` : "No binary required"; }
function Fact({ label, value }: { readonly label: string; readonly value: string }) { return <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2"><div className="text-[0.65rem] font-medium uppercase tracking-wide text-[var(--color-fg-subtle)]">{label}</div><div className="mt-0.5 break-words text-[var(--color-fg)]">{value}</div></div>; }

// packageFileName names the downloaded package after its identity so the
// signature and install commands refer to an unambiguous file.
function packageFileName(envelope: ComponentPackageEnvelope): string {
  const safe = (value: string) => value.trim().replace(/[^A-Za-z0-9._-]+/g, "-") || "package";
  return `${safe(envelope.name)}-${safe(envelope.version)}.package.json`;
}

// downloadText saves the exact string bytes (UTF-8) — the bytes the gateway
// verifies the detached signature against.
function downloadText(text: string, name: string): void {
  saveBlob(new Blob([text], { type: "application/json" }), name);
}
