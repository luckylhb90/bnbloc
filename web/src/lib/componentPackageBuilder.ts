import type { ComponentMCP, ComponentPackageEnvelope, ComponentPackageKind, ComponentRuntime, LocalSettingField } from "./apiTypes";
import type { ArtifactRequirement } from "./componentArtifactRequirements";

export type ComponentPackageDraft = {
  readonly kind: ComponentPackageKind;
  readonly name: string;
  readonly version: string;
  readonly publisher: string;
  readonly description?: string;
  readonly runtime: ComponentRuntime;
  readonly protocol?: { readonly min: string; readonly max: string };
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
  // Plugin-only, carried unchanged from an imported package: the desktop
  // host API capabilities and the desktop-local settings schema.
  readonly host?: readonly string[];
  readonly localSettings?: readonly LocalSettingField[];
  // MCP-only, carried unchanged from an imported package: its tools, env,
  // credentials source and stdio args or http auth.
  readonly mcp?: ComponentMCP;
};

export type ComponentPackageBuildResult =
  | { readonly ok: true; readonly envelope: ComponentPackageEnvelope; readonly artifactRequirements: readonly ArtifactRequirement[] }
  | { readonly ok: false; readonly error: string };

export function buildComponentPackage(draft: ComponentPackageDraft): ComponentPackageBuildResult {
  const name = draft.name.trim();
  const version = draft.version.trim();
  const publisher = draft.publisher.trim();
  if (!name || !version || !publisher) return failure("Name, version, and publisher are required.");
  if (draft.kind === "bnbloc.core.package/v1" && (!draft.protocol?.min.trim() || !draft.protocol.max.trim())) {
    return failure("Core packages require a protocol range.");
  }
  if (draft.kind === "bnbloc.auth-provider.package/v1") {
    if (draft.runtime !== "endpoint") return failure("Auth-provider packages use endpoint runtime.");
    if (draft.contract !== "bnbloc.auth-provider.contract/v1") return failure("Auth-provider contract is required.");
  }
  if (draft.kind === "bnbloc.notifier.package/v1") {
    if (draft.runtime !== "endpoint") return failure("Notifier packages use endpoint runtime.");
    if (draft.contract !== "bnbloc.notifier.contract/v1") return failure("Notifier contract is required.");
  }
  if (draft.kind === "bnbloc.mcp.package/v1") {
    if (draft.runtime !== "stdio" && draft.runtime !== "endpoint") return failure("MCP tool packages use stdio or endpoint runtime.");
    if (!draft.mcp) return failure("MCP tool packages need their mcp block (import the package JSON).");
  }
  if (draft.kind === "bnbloc.plugin.package/v1") {
    if (draft.runtime !== "sidecar") return failure("Plugin packages use sidecar runtime.");
    if (draft.contract !== "bnbloc.plugin.contract/v1") return failure("Plugin contract is required.");
    if (!draft.hooks?.some((hook) => hook.trim())) return failure("Plugin packages require at least one hook.");
  }

  let endpoint: ComponentPackageEnvelope["endpoint"];
  let manifest: unknown;
  let sidecar: ComponentPackageEnvelope["sidecar"];
  let artifacts: ComponentPackageEnvelope["artifacts"];
  let artifactRequirements: readonly ArtifactRequirement[] = [];
  if (draft.runtime === "endpoint") {
    const endpointUrl = draft.endpointUrl?.trim() ?? "";
    if (!endpointUrl) return failure("Endpoint runtime requires a base URL.");
    endpoint = { baseUrl: endpointUrl };
  } else if (draft.runtime === "manifest") {
    if (draft.kind !== "bnbloc.core.package/v1") return failure("Manifest runtime is only supported for core packages.");
    const text = draft.manifestText?.trim() ?? "";
    if (!text) return failure("Manifest runtime requires a drive manifest.");
    try {
      manifest = JSON.parse(text) as unknown;
    } catch (caught: unknown) {
      return failure(caught instanceof SyntaxError ? `Manifest JSON is invalid: ${caught.message}` : "Manifest JSON is invalid.");
    }
  } else if (draft.runtime === "stdio") {
    // A stdio MCP server: one binary per platform, spawned by the agent
    // (no local service URL).
    if (!draft.platformArtifacts?.length) return failure("Select at least one platform binary.");
    artifacts = Object.fromEntries([...new Set(draft.platformArtifacts)].map((platform) => [platform, { url: "", sha256: "" }]));
    artifactRequirements = Object.keys(artifacts).sort((a, b) => a.localeCompare(b)).map((platform) => ({ platform }));
  } else {
    const localUrl = draft.localUrl?.trim() ?? "";
    if (!localUrl) return failure("Sidecar runtime requires a loopback local URL.");
    if (!draft.platformArtifacts?.length) return failure("Select at least one platform binary.");
    sidecar = { localUrl, ...(draft.listenEnv?.trim() ? { listenEnv: draft.listenEnv.trim() } : {}), ...(draft.supervisedEnv?.trim() ? { supervisedEnv: draft.supervisedEnv.trim() } : {}), ...(draft.args?.length ? { args: draft.args } : {}) };
    artifacts = Object.fromEntries([...new Set(draft.platformArtifacts)].map((platform) => [platform, { url: "", sha256: "" }]));
    artifactRequirements = Object.keys(artifacts).sort((a, b) => a.localeCompare(b)).map((platform) => ({ platform }));
    if (draft.kind === "bnbloc.core.package/v1" && draft.manifestText?.trim()) {
      try {
        manifest = JSON.parse(draft.manifestText) as unknown;
      } catch (caught: unknown) {
        return failure(caught instanceof SyntaxError ? `Optional drive manifest JSON is invalid: ${caught.message}` : "Optional drive manifest JSON is invalid.");
      }
    }
  }

  return {
    ok: true,
    envelope: {
      kind: draft.kind,
      name,
      version,
      publisher,
      runtime: draft.runtime,
      ...(draft.description?.trim() ? { description: draft.description.trim() } : {}),
      ...(draft.protocol ? { protocol: draft.protocol } : {}),
      ...(endpoint ? { endpoint } : {}),
      ...(manifest !== undefined ? { manifest } : {}),
      ...(sidecar ? { sidecar } : {}),
      ...(artifacts ? { artifacts } : {}),
      ...(draft.capabilities?.length ? { capabilities: draft.capabilities } : {}),
      ...(draft.network?.length || (isPluginDraft(draft) && draft.host?.length)
        ? { permissions: { ...(draft.network?.length ? { network: draft.network } : {}), ...(isPluginDraft(draft) && draft.host?.length ? { host: draft.host } : {}) } }
        : {}),
      ...(draft.contract ? { contract: draft.contract } : {}),
      ...(draft.hooks?.length ? { hooks: draft.hooks } : {}),
      ...(isPluginDraft(draft) && draft.localSettings?.length ? { localSettings: draft.localSettings } : {}),
      ...(draft.kind === "bnbloc.mcp.package/v1" && draft.mcp ? { mcp: draft.mcp } : {}),
    },
    artifactRequirements,
  };
}

function isPluginDraft(draft: { readonly kind: ComponentPackageKind }): boolean {
  return draft.kind === "bnbloc.plugin.package/v1";
}

function failure(error: string): ComponentPackageBuildResult {
  return { ok: false, error };
}
