// COMPONENT_KINDS is everything the client says about a component kind, one
// row per kind (web M24): a Record, so adding a kind to ComponentKind fails
// to compile until it has its label and package kind. The review panel, the
// package builder, the component cards and the package reader all read it
// (three hand-written label chains called a sandbox an "Auth provider").
import type { ComponentRuntime } from "./wire.gen.ts";

// The executable component kinds the console publishes and lists, and the
// package kind each one's file declares. The server keeps a component's kind
// as a plain string (extreg.Kind); these are the kinds the web knows how to
// present.
export type ComponentKind = "core" | "plugin" | "auth-provider" | "notifier" | "sandbox" | "previewer" | "mcp";
export type ComponentPackageKind =
  | "bnbloc.core.package/v1"
  | "bnbloc.plugin.package/v1"
  | "bnbloc.auth-provider.package/v1"
  | "bnbloc.notifier.package/v1"
  | "bnbloc.sandbox.package/v1"
  | "bnbloc.previewer.package/v1"
  | "bnbloc.mcp.package/v1";

export interface ComponentKindMeta {
  readonly label: string;
  // How a package of the kind is named in a validation error.
  readonly noun: string;
  readonly packageKind: ComponentPackageKind;
  // The contract a package of the kind declares (a core declares a
  // protocol range instead).
  readonly contract?: string;
  // The runtimes a package of the kind may use (absent: any).
  readonly runtimes?: readonly ComponentRuntime[];
  // An endpoint component beside the gateway: it holds a credential the
  // gateway shows once at install and a super admin rotates, and asks for
  // its settings with it.
  readonly credential?: boolean;
}

export const COMPONENT_KINDS: Readonly<Record<ComponentKind, ComponentKindMeta>> = {
  core: { label: "Core", noun: "core", packageKind: "bnbloc.core.package/v1" },
  plugin: { label: "Plugin", noun: "plugin", packageKind: "bnbloc.plugin.package/v1", contract: "bnbloc.plugin.contract/v1", runtimes: ["sidecar"] },
  "auth-provider": { label: "Auth provider", noun: "auth-provider", packageKind: "bnbloc.auth-provider.package/v1", contract: "bnbloc.auth-provider.contract/v1", runtimes: ["endpoint"], credential: true },
  notifier: { label: "Notifier", noun: "notifier", packageKind: "bnbloc.notifier.package/v1", contract: "bnbloc.notifier.contract/v1", runtimes: ["endpoint"], credential: true },
  sandbox: { label: "Sandbox", noun: "sandbox", packageKind: "bnbloc.sandbox.package/v1", contract: "bnbloc.sandbox.contract/v1", runtimes: ["sidecar", "endpoint"] },
  previewer: { label: "Previewer", noun: "previewer", packageKind: "bnbloc.previewer.package/v1", contract: "bnbloc.previewer.contract/v1", runtimes: ["bundle"] },
  mcp: { label: "MCP tool", noun: "MCP", packageKind: "bnbloc.mcp.package/v1", contract: "bnbloc.mcp.contract/v1", runtimes: ["stdio", "endpoint"] },
};

const KINDS = Object.entries(COMPONENT_KINDS) as [ComponentKind, (typeof COMPONENT_KINDS)[ComponentKind]][];

// componentKindOfPackage is the kind of a package kind string (undefined for
// anything that is not one).
export function componentKindOfPackage(packageKind: unknown): ComponentKind | undefined {
  return KINDS.find(([, m]) => m.packageKind === packageKind)?.[0];
}

// componentKindLabel names a kind (a short kind or a package kind); an
// unknown one shows as itself rather than as some other kind.
export function componentKindLabel(kind: string): string {
  const short = (kind in COMPONENT_KINDS ? kind : componentKindOfPackage(kind)) as ComponentKind | undefined;
  return short ? COMPONENT_KINDS[short].label : kind;
}

// holdsCredential reports a kind whose components hold a credential (an
// endpoint component beside the gateway: a notifier, an auth provider).
export function holdsCredential(kind: string): boolean {
  return kind in COMPONENT_KINDS && COMPONENT_KINDS[kind as ComponentKind].credential === true;
}
