import { useMemo, useState } from "react";
import type { PackageDraftFields } from "@/lib/componentPublishWizard";
import { ArrowLeftIcon, ArrowRightIcon } from "@phosphor-icons/react";
import type { ComponentPackageEnvelope, ComponentRollout } from "@/lib/apiTypes";
import { buildComponentPackage } from "@/lib/componentPackageBuilder";
import { Button } from "@/ui/Button";
import { Field, Input, Textarea } from "@/ui/Input";
import { ComponentRolloutOptions } from "@/views/ComponentRolloutOptions";

const PLATFORMS = ["darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64", "windows-amd64"] as const;

export function ComponentPackageBuilderForm({ initialDraft, initialEnvelope, onBack, onContinue }: {
  readonly initialDraft: PackageDraftFields;
  readonly initialEnvelope?: ComponentPackageEnvelope;
  readonly onBack: () => void;
  readonly onContinue: (envelope: ComponentPackageEnvelope, rollout: ComponentRollout) => void;
}) {
  const [name, setName] = useState(initialDraft.name);
  const [version, setVersion] = useState(initialDraft.version);
  const [publisher, setPublisher] = useState(initialDraft.publisher);
  const [description, setDescription] = useState(initialDraft.description ?? "");
  const [protocolMin, setProtocolMin] = useState(initialDraft.protocolMin);
  const [protocolMax, setProtocolMax] = useState(initialDraft.protocolMax);
  const [endpointUrl, setEndpointUrl] = useState(initialDraft.endpointUrl ?? "");
  const [manifestText, setManifestText] = useState(initialDraft.manifestText ?? "");
  const [localUrl, setLocalUrl] = useState(initialDraft.localUrl ?? "");
  const [listenEnv, setListenEnv] = useState(initialDraft.listenEnv ?? "");
  const [supervisedEnv, setSupervisedEnv] = useState(initialDraft.supervisedEnv ?? "");
  const [argsText, setArgsText] = useState((initialDraft.args ?? []).join("\n"));
  const [platforms, setPlatforms] = useState<readonly string[]>(initialDraft.platformArtifacts ?? []);
  const [networkText, setNetworkText] = useState((initialDraft.network ?? []).join("\n"));
  const [capabilityText, setCapabilityText] = useState((initialDraft.capabilities ?? []).join("\n"));
  const [contract, setContract] = useState(initialDraft.contract ?? "");
  const [hooksText, setHooksText] = useState((initialDraft.hooks ?? []).join("\n"));
  const [rollout, setRollout] = useState<ComponentRollout>("auto");
  const [advanced, setAdvanced] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const descriptionLine = useMemo(() => `${packageKindLabel(initialDraft.kind)} · ${runtimeLabel(initialDraft.runtime)}`, [initialDraft.kind, initialDraft.runtime]);
  const isCore = initialDraft.kind === "bnbloc.core.package/v1";
  const isPlugin = initialDraft.kind === "bnbloc.plugin.package/v1";

  const continueToArtifacts = () => {
    const result = buildComponentPackage({
      kind: initialDraft.kind,
      name,
      version,
      publisher,
      description,
      runtime: initialDraft.runtime,
      ...(isCore ? { protocol: { min: protocolMin, max: protocolMax } } : {}),
      ...(initialDraft.runtime === "endpoint" ? { endpointUrl } : {}),
      ...(initialDraft.runtime === "manifest" || (initialDraft.runtime === "sidecar" && isCore && manifestText.trim()) ? { manifestText } : {}),
      ...(initialDraft.runtime === "sidecar" ? { localUrl, listenEnv, supervisedEnv, args: splitLines(argsText), platformArtifacts: platforms } : {}),
      ...(networkText.trim() ? { network: splitLines(networkText) } : {}),
      ...(capabilityText.trim() ? { capabilities: splitLines(capabilityText) } : {}),
      ...(contract.trim() ? { contract: contract.trim() } : {}),
      ...(hooksText.trim() ? { hooks: splitLines(hooksText) } : {}),
      ...(initialDraft.host ? { host: initialDraft.host } : {}),
      ...(initialDraft.localSettings ? { localSettings: initialDraft.localSettings } : {}),
    });
    if (!result.ok) { setError(result.error); return; }
    setError(null);
    onContinue(result.envelope, rollout);
  };

  return (
    <section className="grid gap-4" aria-label="Configure component">
      <div><p className="text-xs font-medium uppercase tracking-wide text-[var(--color-accent)]">Step 2 of 4 · Configure</p><h4 className="mt-1 text-base font-semibold text-[var(--color-fg)]">Check the package details</h4><p className="mt-1 text-sm text-[var(--color-fg-muted)]">We filled in what we could. Complete anything missing, then continue.</p><p className="mt-2 text-xs text-[var(--color-fg-subtle)]">{descriptionLine}</p></div>
      <div className="grid gap-3 sm:grid-cols-3"><Field label="Name"><Input autoFocus value={name} onChange={(event) => setName(event.target.value.toLowerCase())} placeholder="component-name" /></Field><Field label="Version"><Input value={version} onChange={(event) => setVersion(event.target.value)} /></Field><Field label="Publisher"><Input value={publisher} onChange={(event) => setPublisher(event.target.value)} placeholder="publisher-id" /></Field></div>
      {isCore && <div className="grid gap-3 sm:grid-cols-2"><Field label="Protocol minimum"><Input value={protocolMin} onChange={(event) => setProtocolMin(event.target.value)} /></Field><Field label="Protocol maximum"><Input value={protocolMax} onChange={(event) => setProtocolMax(event.target.value)} /></Field></div>}
      {initialDraft.runtime === "endpoint" && <Field label="Service URL" hint="The Gateway checks this service when you publish."><Input value={endpointUrl} onChange={(event) => setEndpointUrl(event.target.value)} placeholder="https://service.example" /></Field>}
      {initialDraft.runtime === "manifest" && <Field label="Core manifest JSON"><Textarea rows={7} value={manifestText} onChange={(event) => setManifestText(event.target.value)} placeholder="Paste the manifest REST contract JSON" className="font-mono text-xs" spellCheck={false} /></Field>}
      {initialDraft.runtime === "sidecar" && <><Field label="Local service URL" hint="The desktop starts this binary locally. Use its loopback listen URL."><Input value={localUrl} onChange={(event) => setLocalUrl(event.target.value)} placeholder="http://127.0.0.1:7099" /></Field>{isPlugin && <div className="grid gap-3 sm:grid-cols-2"><Field label="Plugin contract"><Input value={contract} onChange={(event) => setContract(event.target.value)} placeholder="bnbloc.plugin.contract/v1" /></Field><Field label="Event hooks (one per line)"><Textarea rows={3} value={hooksText} onChange={(event) => setHooksText(event.target.value)} placeholder="chat_message.v1" className="font-mono text-xs" /></Field></div>}<Field label="Which platforms have a binary?"><div className="grid gap-2 sm:grid-cols-2">{PLATFORMS.map((platform) => <label key={platform} className="flex items-center gap-2 rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-2 text-sm"><input type="checkbox" checked={platforms.includes(platform)} onChange={(event) => setPlatforms((current) => event.target.checked ? [...current, platform] : current.filter((item) => item !== platform))} />{platform}</label>)}</div></Field></>}
      {initialDraft.kind === "bnbloc.auth-provider.package/v1" && <Field label="Auth-provider contract"><Input value={contract} onChange={(event) => setContract(event.target.value)} placeholder="bnbloc.auth-provider.contract/v1" /></Field>}
      <div className="border-t border-[var(--color-border)] pt-3"><button type="button" className="text-sm font-medium text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]" aria-expanded={advanced} onClick={() => setAdvanced((current) => !current)}>{advanced ? "Hide optional settings" : "Optional settings"}</button>{advanced && <div className="mt-3 grid gap-3"><Field label="Description"><Input value={description} onChange={(event) => setDescription(event.target.value)} placeholder="What this component provides" /></Field>{initialDraft.runtime === "sidecar" && <><Field label="Listen environment variable"><Input value={listenEnv} onChange={(event) => setListenEnv(event.target.value)} /></Field><Field label="Supervised environment variable" hint="Set to 1 at launch; the sidecar should exit when its stdin closes."><Input value={supervisedEnv} onChange={(event) => setSupervisedEnv(event.target.value)} /></Field><Field label="Binary launch arguments (one per line)"><Textarea rows={2} value={argsText} onChange={(event) => setArgsText(event.target.value)} className="font-mono text-xs" /></Field></>}{initialDraft.kind === "bnbloc.core.package/v1" && <Field label="Legacy REST manifest (optional)"><Textarea rows={4} value={manifestText} onChange={(event) => setManifestText(event.target.value)} placeholder="Core Protocol sidecars may leave this empty." className="font-mono text-xs" spellCheck={false} /></Field>}<Field label="Network permissions (one per line)"><Textarea rows={2} value={networkText} onChange={(event) => setNetworkText(event.target.value)} className="font-mono text-xs" /></Field><Field label="Capabilities (one per line)"><Textarea rows={2} value={capabilityText} onChange={(event) => setCapabilityText(event.target.value)} className="font-mono text-xs" /></Field></div>}</div>
      <Field label="Rollout"><ComponentRolloutOptions value={rollout} onChange={setRollout} /></Field>
      {error && <p role="alert" className="text-sm text-[var(--color-danger)]">{error}</p>}
      <div className="flex justify-between gap-2 border-t border-[var(--color-border)] pt-4"><Button variant="ghost" onClick={onBack}><ArrowLeftIcon size={15} /> Back</Button><Button variant="primary" onClick={continueToArtifacts}>Continue <ArrowRightIcon size={15} /></Button></div>
      {initialEnvelope && <p className="text-xs text-[var(--color-fg-subtle)]">Imported package fields are editable. Existing binaries must be uploaded again before signing.</p>}
    </section>
  );
}

function splitLines(value: string): string[] { return value.split(/\r?\n/).map((line) => line.trim()).filter((line) => line !== ""); }
function packageKindLabel(kind: PackageDraftFields["kind"]): string { return kind === "bnbloc.core.package/v1" ? "Core" : kind === "bnbloc.plugin.package/v1" ? "Plugin" : kind === "bnbloc.notifier.package/v1" ? "Notifier" : "Authentication provider"; }
function runtimeLabel(runtime: PackageDraftFields["runtime"]): string { return runtime === "endpoint" ? "Service URL" : runtime === "manifest" ? "Core manifest" : "Local sidecar binary"; }
