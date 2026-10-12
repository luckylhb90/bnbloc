import type { ComponentMCP, ComponentPackageEnvelope, ComponentPackageKind, ComponentRuntime, LocalSettingField } from "./apiTypes";
import { COMPONENT_KINDS, componentKindOfPackage } from "./componentKinds.ts";

export type PublishWizardStep = "source" | "configure" | "artifacts" | "publish";

export interface PackageDraftFields {
  readonly kind: ComponentPackageKind;
  readonly runtime: ComponentRuntime;
  readonly name: string;
  readonly version: string;
  readonly publisher: string;
  readonly description?: string;
  readonly protocolMin: string;
  readonly protocolMax: string;
  readonly endpointUrl?: string;
  readonly manifestText?: string;
  readonly localUrl?: string;
  readonly listenEnv?: string;
  readonly supervisedEnv?: string;
  readonly args?: readonly string[];
  readonly platformArtifacts?: readonly string[];
  readonly network?: readonly string[];
  readonly capabilities?: readonly string[];
  readonly contract?: string;
  readonly hooks?: readonly string[];
  readonly host?: readonly string[];
  readonly localSettings?: readonly LocalSettingField[];
  // An MCP tool's block, carried unchanged from the imported package.
  readonly mcp?: ComponentMCP;
}

// takesBinaries: the runtimes whose platform binaries the wizard uploads
// (a sidecar's, a stdio MCP server's).
export function takesBinaries(runtime: ComponentRuntime | undefined): boolean {
  return runtime === "sidecar" || runtime === "stdio";
}

export interface WizardAdvanceState {
  readonly sourceReady?: boolean;
  readonly runtime?: ComponentRuntime;
  readonly artifactsReady?: boolean;
}

export function nextPublishWizardStep(step: PublishWizardStep, state: WizardAdvanceState): PublishWizardStep | null {
  if (step === "source") return state.sourceReady ? "configure" : null;
  if (step === "configure") return takesBinaries(state.runtime) ? "artifacts" : state.runtime ? "publish" : null;
  if (step === "artifacts") return state.artifactsReady ? "publish" : null;
  return null;
}

export function previousPublishWizardStep(step: PublishWizardStep, runtime: ComponentRuntime | undefined): PublishWizardStep | null {
  if (step === "configure") return "source";
  if (step === "artifacts") return "configure";
  if (step === "publish") return takesBinaries(runtime) ? "artifacts" : "configure";
  return null;
}

export function createPackageDraft(kind: ComponentPackageKind, runtime: ComponentRuntime): PackageDraftFields {
  const short = componentKindOfPackage(kind);
  const contract = short ? COMPONENT_KINDS[short].contract : undefined;
  return {
    kind, runtime, name: "", version: "0.1.0", publisher: "", protocolMin: "1.0", protocolMax: "1.0",
    platformArtifacts: takesBinaries(runtime) ? [] : undefined, contract, hooks: kind === "bnbloc.plugin.package/v1" ? [] : undefined,
  };
}

export function packageDraftFromEnvelope(envelope: ComponentPackageEnvelope): PackageDraftFields {
  return {
    kind: envelope.kind, runtime: envelope.runtime, name: envelope.name, version: envelope.version, publisher: envelope.publisher,
    description: envelope.description ?? "", protocolMin: envelope.protocol?.min ?? "1.0", protocolMax: envelope.protocol?.max ?? "1.0",
    endpointUrl: envelope.endpoint?.baseUrl ?? "", manifestText: envelope.manifest === undefined ? undefined : JSON.stringify(envelope.manifest, null, 2),
    localUrl: envelope.sidecar?.localUrl ?? "", listenEnv: envelope.sidecar?.listenEnv ?? "", supervisedEnv: envelope.sidecar?.supervisedEnv ?? "", args: envelope.sidecar?.args,
    platformArtifacts: Object.keys(envelope.artifacts ?? {}), network: envelope.permissions?.network, capabilities: envelope.capabilities,
    contract: envelope.contract, hooks: envelope.hooks, host: envelope.permissions?.host, localSettings: envelope.localSettings,
    ...(envelope.mcp ? { mcp: envelope.mcp } : {}),
  };
}
