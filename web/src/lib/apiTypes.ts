// Component types. Every shape the gateway sends or reads is generated from
// internal/wire (wire.gen.ts) and only re-exported here (web boundary rule
// T8): the component projections (wire/components.go) and the package parts
// they carry (wire/component_shapes.go). What stays below is the web's own:
// the package file it reads and builds before upload, and the publish
// wizard's step state.
import type {
  ComponentArtifact,
  ComponentEndpoint,
  ComponentMCP,
  ComponentPermissions,
  ComponentPreviewer,
  ComponentProtocolRange,
  ComponentRuntime,
  ComponentSetting,
  ComponentSidecar,
} from "./wire.gen.ts";
import type { ComponentPackageKind } from "./componentKinds.ts";

export type {
  ComponentArtifact,
  ComponentEndpoint,
  ComponentInstallRequest,
  ComponentKey,
  ComponentKeysReply,
  ComponentMCP,
  ComponentPermissions,
  ComponentPreviewer,
  ComponentProtocolRange,
  ComponentRollout,
  ComponentRolloutRequest,
  ComponentRuntime,
  ComponentSetting,
  ComponentSidecar,
  ComponentSidecarFile,
  ComponentsReply,
  MemberComponent,
  PlatformComponent,
  PlatformComponentsReply,
  PublishedComponent,
} from "./wire.gen.ts";

// ComponentPackageEnvelope is a component package file (core/pkgmeta
// .Package) as the publish flow reads and builds it before upload: the
// server verifies the signed text, so this is the web's reading of it, made
// of the generated package parts.
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
  readonly settings?: readonly ComponentSetting[];
  readonly previewer?: ComponentPreviewer;
  readonly mcp?: ComponentMCP;
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
