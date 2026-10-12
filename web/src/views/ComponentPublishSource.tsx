import { useRef, useState } from "react";
import { Select } from "@/ui/Select";
import type { ComponentRuntime } from "@/lib/apiTypes";
import type { ComponentPackageKind } from "@/lib/componentKinds";
import type { InspectedComponentFile } from "@/lib/componentPackageIntake";
import { Button } from "@/ui/Button";
import { Field } from "@/ui/Input";
import { DropZone } from "@/views/ComponentUploadDropZone";

export const SIDECAR_PLATFORMS = ["darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64", "windows-amd64"] as const;
export type PublishSourceMode = { readonly kind: ComponentPackageKind; readonly runtime: ComponentRuntime; readonly label: string; readonly description: string };
export type BinaryContract = { readonly kind: ComponentPackageKind; readonly runtime: "sidecar"; readonly label: string; readonly description: string };

const MANUAL_MODES: readonly PublishSourceMode[] = [
  { kind: "bnbloc.core.package/v1", runtime: "endpoint", label: "Core Protocol service", description: "Connect an already-running service endpoint." },
  { kind: "bnbloc.core.package/v1", runtime: "manifest", label: "REST manifest core", description: "Describe a core that exposes a REST API." },
  { kind: "bnbloc.plugin.package/v1", runtime: "sidecar", label: "Room-event plugin", description: "Build a local plugin for registered room-event hooks." },
  { kind: "bnbloc.auth-provider.package/v1", runtime: "endpoint", label: "Auth-provider service", description: "Connect a deployed service implementing the bnbloc auth-provider contract." },
];
const BINARY_MODES: readonly BinaryContract[] = [
  { kind: "bnbloc.core.package/v1", runtime: "sidecar", label: "Core Protocol sidecar", description: "Choose this only when this program implements Core Protocol." },
  { kind: "bnbloc.plugin.package/v1", runtime: "sidecar", label: "Room-event plugin", description: "Choose this only when this program implements a registered plugin contract." },
];

export function ComponentPublishSource({ onFile, onManual, onBinaryContract, inspection, pendingBinary }: {
  readonly onFile: (file: File) => void;
  readonly onManual: (mode: PublishSourceMode, inspection?: InspectedComponentFile) => void;
  readonly onBinaryContract: (file: File, mode: BinaryContract, platform?: string) => void;
  readonly inspection?: InspectedComponentFile | null;
  readonly pendingBinary?: File | null;
}) {
  const fileInput = useRef<HTMLInputElement | null>(null);
  const [dragging, setDragging] = useState(false);
  const [otherContracts, setOtherContracts] = useState(false);
  const [platform, setPlatform] = useState(inspection?.type === "binary" ? inspection.detectedPlatform ?? "" : "");
  return <section className="grid gap-5" aria-label="Choose how to publish">
    <div><p className="text-xs font-medium uppercase tracking-wide text-[var(--color-accent)]">Step 1 of 4 · Add one input</p><h4 className="mt-1 text-base font-semibold text-[var(--color-fg)]">What are you starting with?</h4><p className="mt-1 text-sm text-[var(--color-fg-muted)]">Upload one file. We inspect its structure and show the next required task.</p></div>
    <DropZone dragging={dragging} setDragging={setDragging} inputRef={fileInput} onFile={async (file) => onFile(file)} />
    {!inspection && <div className="border-t border-[var(--color-border)] pt-4"><p className="text-sm font-medium text-[var(--color-fg)]">Prefer to enter a service contract manually?</p><div className="mt-3 grid gap-2 sm:grid-cols-2">{MANUAL_MODES.slice(0, 2).map((mode) => <ModeButton key={`${mode.kind}:${mode.runtime}`} mode={mode} onSelect={() => onManual(mode)} />)}</div><Button className="mt-3" variant="ghost" size="sm" aria-expanded={otherContracts} onClick={() => setOtherContracts((open) => !open)}>{otherContracts ? "Hide other contracts" : "Other service contracts"}</Button>{otherContracts && <div className="mt-2 grid gap-2 sm:grid-cols-2">{MANUAL_MODES.slice(2).map((mode) => <ModeButton key={`${mode.kind}:${mode.runtime}`} mode={mode} onSelect={() => onManual(mode)} />)}</div>}</div>}
    {inspection && <InspectionCard inspection={inspection} pendingBinary={pendingBinary} platform={platform} setPlatform={setPlatform} onChooseAgain={() => fileInput.current?.click()} onManual={onManual} onBinaryContract={onBinaryContract} />}
    <p className="text-xs text-[var(--color-fg-subtle)]">We inspect declared package structure and file headers. The filename never determines component behavior.</p>
  </section>;
}

function ModeButton({ mode, onSelect }: { readonly mode: PublishSourceMode | BinaryContract; readonly onSelect: () => void }) {
  return <button type="button" className="tactile rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 py-3 text-left hover:border-[var(--color-border-strong)]" onClick={onSelect}><span className="block text-sm font-medium text-[var(--color-fg)]">{mode.label}</span><span className="mt-1 block text-xs leading-relaxed text-[var(--color-fg-muted)]">{mode.description}</span></button>;
}

function InspectionCard({ inspection, pendingBinary, platform, setPlatform, onChooseAgain, onManual, onBinaryContract }: {
  readonly inspection: InspectedComponentFile;
  readonly pendingBinary?: File | null;
  readonly platform: string;
  readonly setPlatform: (platform: string) => void;
  readonly onChooseAgain: () => void;
  readonly onManual: (mode: PublishSourceMode, inspection?: InspectedComponentFile) => void;
  readonly onBinaryContract: (file: File, mode: BinaryContract, platform?: string) => void;
}) {
  if (inspection.type === "package") return <div role="status" className="rounded-[var(--radius)] border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] p-3 text-sm">Package contract recognized. Next: review its declared values and fill any missing fields.</div>;
  if (inspection.type === "binary") return <section className="grid gap-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-4"><div><p className="text-xs font-medium uppercase tracking-wide text-[var(--color-accent)]">Executable detected</p><h5 className="mt-1 text-sm font-semibold text-[var(--color-fg)]">What contract does this implement?</h5><p className="mt-1 text-xs text-[var(--color-fg-muted)]">{inspection.detectedPlatform ? `File header confirms ${inspection.detectedPlatform}.` : "We could not determine the platform from the header; choose the build target."} The filename is not used to guess its purpose.</p></div>{!inspection.detectedPlatform && <Field label="Build platform"><Select className="h-10 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-3 text-sm" value={platform} onChange={(event) => setPlatform(event.target.value)}><option value="">Choose platform</option>{SIDECAR_PLATFORMS.map((target) => <option key={target} value={target}>{target}</option>)}</Select></Field>}<div className="grid gap-2">{BINARY_MODES.map((mode) => <ModeButton key={mode.kind} mode={mode} onSelect={() => pendingBinary && onBinaryContract(pendingBinary, mode, inspection.detectedPlatform ?? (platform || undefined))} />)}</div></section>;
  if (inspection.type === "external-metadata") return <section className="grid gap-3 rounded-[var(--radius-lg)] border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 p-4" role="status"><div><p className="text-xs font-medium uppercase tracking-wide text-[var(--color-warning)]">Identity-provider metadata recognized</p><p className="mt-1 text-sm text-[var(--color-fg)]">{inspection.message}</p>{inspection.entityId && <p className="mt-2 break-all font-mono text-xs text-[var(--color-fg-muted)]">Entity ID: {inspection.entityId}</p>}</div><button type="button" className="w-fit text-sm font-medium text-[var(--color-accent)] underline" onClick={() => onManual(MANUAL_MODES[3]!, inspection)}>I already have an auth-provider service</button></section>;
  if (inspection.type === "unsupported-package") return <section className="grid gap-3 rounded-[var(--radius-lg)] border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-4" role="alert"><div><h5 className="text-sm font-semibold text-[var(--color-fg)]">This JSON isn’t an installable component package</h5><p className="mt-1 text-sm text-[var(--color-danger)]">{inspection.message}</p></div><div className="flex flex-wrap gap-3"><button type="button" className="text-sm text-[var(--color-accent)] underline" onClick={onChooseAgain}>Choose another file</button><button type="button" className="text-sm text-[var(--color-accent)] underline" onClick={() => onManual(MANUAL_MODES[0]!, inspection)}>Enter a supported service manually</button></div></section>;
  return <section className="flex flex-wrap items-center justify-between gap-3 rounded-[var(--radius-lg)] border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-4" role="alert"><div><h5 className="text-sm font-semibold text-[var(--color-fg)]">{inspection.type === "sensitive-credential" ? "Secret file not accepted" : inspection.type === "invalid-json" ? "Couldn’t read this package file" : "File structure not recognized"}</h5><p className="mt-1 text-sm text-[var(--color-danger)]">{inspection.message}</p></div><button type="button" className="text-sm font-medium text-[var(--color-accent)] underline" onClick={onChooseAgain}>Choose another file</button></section>;
}
