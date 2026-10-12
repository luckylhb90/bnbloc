export type ComponentKind = "core" | "plugin" | "auth-provider" | "notifier" | "sandbox" | "previewer" | "mcp";
export type ComponentPackageKind =
  | "bnbloc.core.package/v1"
  | "bnbloc.plugin.package/v1"
  | "bnbloc.auth-provider.package/v1"
  | "bnbloc.notifier.package/v1"
  | "bnbloc.sandbox.package/v1"
  | "bnbloc.previewer.package/v1"
  | "bnbloc.mcp.package/v1";
export type ComponentRuntime = "endpoint" | "manifest" | "sidecar" | "bundle" | "stdio";
export type ComponentRollout = "auto" | "optin" | "off";

export interface ComponentProtocolRange {
  readonly min: string;
  readonly max: string;
}

export interface ComponentEndpoint {
  readonly baseUrl: string;
}

export interface ComponentArtifact {
  readonly url: string;
  readonly sha256: string;
}

export interface ComponentPermissions {
  readonly network?: readonly string[];
  // Desktop plugin host API capabilities (plugin packages only), e.g. "chat".
  readonly host?: readonly string[];
}

export interface ComponentSidecar {
  readonly listenEnv?: string;
  readonly supervisedEnv?: string;
  readonly localUrl: string;
  readonly args?: readonly string[];
  /** Env var the desktop sets to a fresh per-launch bearer token. */
  readonly tokenEnv?: string;
  /** Env var naming the per-component private data directory. */
  readonly dataDirEnv?: string;
  /** Extra per-platform artifacts, published as artifacts["<platform>/<name>"]. */
  readonly files?: readonly ComponentSidecarFile[];
}

export interface ComponentSidecarFile {
  readonly name: string;
  readonly env: string;
  readonly executable?: boolean;
}

// One field of a plugin's desktop-local settings schema (e.g. a personal
// bot's credentials). The values stay on the member's desktop.
export interface LocalSettingField {
  readonly key: string;
  readonly label: string;
  readonly help?: string;
  readonly secret?: boolean;
  readonly required?: boolean;
}

// A previewer component's payload (bnbloc.previewer.package/v1): the files it
// renders, its bundle entry, rank, capabilities and declared toolbar. The
// member catalog adds the pinned bundle and the browser frame base
// (lib/previewerFrame parses that form).
export interface ComponentPreviewer {
  readonly entry: string;
  readonly match: { readonly mimeTypes?: readonly string[]; readonly extensions?: readonly string[] };
  readonly rank: number;
  readonly capabilities?: readonly string[];
  readonly toolbar?: readonly { readonly id: string; readonly icon: string; readonly label: string; readonly kind: string }[];
}

// An MCP tool component's payload (bnbloc.mcp.package/v1): its tools (for
// display), the variables it needs and where their values come from
// (member: each member's desktop; org: an org admin's connector), and the
// stdio args or http auth header. Values never ship in a package.
export interface ComponentMCPEnv {
  readonly name: string;
  readonly description?: string;
  readonly secret?: boolean;
  readonly required?: boolean;
}

export interface ComponentMCP {
  readonly tools: readonly { readonly name: string; readonly description?: string }[];
  readonly env?: readonly ComponentMCPEnv[];
  readonly credentials?: "member" | "org";
  readonly stdio?: { readonly args?: readonly string[] };
  // scheme "oauth": each member signs in from their desktop (clientId and
  // scopes are optional hints for the sign-in server).
  readonly http?: {
    readonly auth: { readonly scheme: string; readonly header?: string; readonly env?: string; readonly clientId?: string; readonly scopes?: readonly string[] };
  };
}

// The member catalog adds the transport and, for org credentials, whether
// the member's organization configured its connector.
export interface MemberComponentMCP extends ComponentMCP {
  readonly transport?: "stdio" | "http";
  readonly orgConfigured?: boolean;
}

export interface ComponentTrust {
  readonly keyId?: string;
  readonly contentHash?: string;
  readonly signed: boolean;
}

export interface ComponentAdoption {
  readonly memberInstalls: number;
  readonly totalMembers?: number;
}

export interface ComponentKey {
  readonly keyId: string;
  readonly publicKey: string;
  readonly label?: string;
  readonly addedAtMs?: number;
  readonly addedBy?: string;
}

export interface ComponentBase {
  readonly kind: ComponentKind;
  readonly name: string;
  readonly version: string;
  readonly publisher?: string;
  readonly description?: string;
  readonly runtime?: ComponentRuntime;
  readonly requiresDesktop?: boolean;
  readonly rollout: ComponentRollout;
  readonly capabilities?: readonly string[];
  readonly permissions?: ComponentPermissions;
  readonly hooks?: readonly string[];
  readonly artifacts?: Readonly<Record<string, ComponentArtifact>>;
  readonly sidecar?: ComponentSidecar;
  readonly endpoint?: ComponentEndpoint;
  readonly previewer?: ComponentPreviewer;
  readonly mcp?: MemberComponentMCP;
}

export interface PlatformComponent extends ComponentBase {
  readonly publisher: string;
  readonly runtime: ComponentRuntime;
  readonly protocol?: ComponentProtocolRange;
  readonly manifest?: unknown;
  readonly keyId?: string;
  readonly contentHash?: string;
  readonly installedAt?: number;
  readonly trust: ComponentTrust;
  readonly adoption: ComponentAdoption;
  readonly memberInstalls?: number;
  readonly totalMembers?: number;
}

// The install answer: a notifier's first install carries its signing secret,
// shown this once.
export interface PublishedComponent extends PlatformComponent {
  readonly secret?: string;
}

export interface MemberComponent extends ComponentBase {
  readonly rollout: "auto" | "optin";
  readonly installed: boolean;
  readonly managed: boolean;
  readonly installedAtMs?: number;
  readonly membersInstalled?: number;
}

export interface ComponentPackageEnvelope {
  readonly kind: ComponentPackageKind;
  readonly name: string;
  readonly version: string;
  readonly publisher: string;
  readonly description?: string;
  readonly runtime: ComponentRuntime;
  readonly protocol?: ComponentProtocolRange;
  readonly endpoint?: ComponentEndpoint;
  readonly manifest?: unknown;
  readonly sidecar?: ComponentSidecar;
  readonly artifacts?: Readonly<Record<string, ComponentArtifact>>;
  readonly capabilities?: readonly string[];
  readonly permissions?: ComponentPermissions;
  readonly contract?: string;
  readonly hooks?: readonly string[];
  readonly localSettings?: readonly LocalSettingField[];
  readonly previewer?: ComponentPreviewer;
  readonly mcp?: ComponentMCP;
}

export interface ComponentPublishRequest {
  readonly package: string;
  readonly signature: string;
  readonly rollout?: ComponentRollout;
}
export type ComponentInstallRequest = ComponentPublishRequest;

export interface ComponentRolloutRequest {
  readonly rollout: ComponentRollout;
}

export type ComponentValidationStepId =
  | "reading"
  | "schema"
  | "signature"
  | "artifact"
  | "probe"
  | "review"
  | "publishing";

export type ComponentValidationStepState = "pending" | "running" | "done" | "failed";

export interface ComponentValidationStep {
  readonly id: ComponentValidationStepId;
  readonly label: string;
  readonly state: ComponentValidationStepState;
  readonly detail: string;
}
