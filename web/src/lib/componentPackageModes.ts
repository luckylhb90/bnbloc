import type { ComponentPackageKind, ComponentRuntime } from "./apiTypes";

export type ComponentPackageBuildMode = {
  readonly kind: ComponentPackageKind;
  readonly runtime: ComponentRuntime;
  readonly label: string;
  readonly key: `${ComponentPackageKind}:${ComponentRuntime}`;
};

export const COMPONENT_PACKAGE_BUILD_MODES: readonly ComponentPackageBuildMode[] = [
  { kind: "bnbloc.core.package/v1", runtime: "endpoint", label: "Core endpoint", key: "bnbloc.core.package/v1:endpoint" },
  { kind: "bnbloc.core.package/v1", runtime: "manifest", label: "Core manifest", key: "bnbloc.core.package/v1:manifest" },
  { kind: "bnbloc.core.package/v1", runtime: "sidecar", label: "Core sidecar", key: "bnbloc.core.package/v1:sidecar" },
  { kind: "bnbloc.plugin.package/v1", runtime: "sidecar", label: "Plugin sidecar", key: "bnbloc.plugin.package/v1:sidecar" },
  { kind: "bnbloc.auth-provider.package/v1", runtime: "endpoint", label: "Auth-provider endpoint", key: "bnbloc.auth-provider.package/v1:endpoint" },
];

export function buildComponentPackageModes(): readonly ComponentPackageBuildMode[] {
  return COMPONENT_PACKAGE_BUILD_MODES;
}
