import { buildComponentPackageModes, type ComponentPackageBuildMode } from "./componentPackageModes.ts";
import { buildComponentPackage, type ComponentPackageDraft } from "./componentPackageBuilder.ts";
import { buildFinalComponentPackageJSON } from "./componentArtifactRequirements.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function testEndpointCoreBuildsWithoutArtifacts(): void {
  const result = buildComponentPackage({ kind: "bnbloc.core.package/v1", name: "remote-core", version: "1.2.3", publisher: "platform", description: "Remote Core", runtime: "endpoint", protocol: { min: "1.0", max: "1.0" }, endpointUrl: "https://core.example", network: ["core.example"] });
  assert(result.ok, "valid endpoint core builds");
  if (!result.ok) return;
  assert(result.envelope.endpoint?.baseUrl === "https://core.example", "endpoint URL is included");
  assert(result.envelope.artifacts === undefined, "endpoint package has no binary artifacts");
}

function testManifestCoreRequiresAndPreservesManifest(): void {
  const draft: ComponentPackageDraft = { kind: "bnbloc.core.package/v1", name: "manifest-core", version: "1.0.0", publisher: "platform", runtime: "manifest", protocol: { min: "1.0", max: "1.0" }, manifestText: "" };
  const rejected = buildComponentPackage(draft);
  assert(!rejected.ok && rejected.error.includes("manifest"), "manifest runtime requires drive contract JSON");
  const valid = buildComponentPackage({ ...draft, manifestText: "{\"name\":\"manifest-core\"}" });
  assert(valid.ok, "manifest core builds with JSON contract");
  if (!valid.ok) return;
  const manifest = valid.envelope.manifest;
  assert(typeof manifest === "object" && manifest !== null && "name" in manifest && manifest.name === "manifest-core", "manifest contract retained");
}

function testProtocolSidecarBuildsWithoutManifest(): void {
  const result = buildComponentPackage({ kind: "bnbloc.core.package/v1", name: "samplecore", version: "0.1.0", publisher: "platform", runtime: "sidecar", protocol: { min: "1.0", max: "1.0" }, localUrl: "http://127.0.0.1:7099", listenEnv: "BNBLOC_SAMPLECORE_LISTEN", supervisedEnv: "BNBLOC_SAMPLECORE_SUPERVISED", network: ["127.0.0.1", "localhost"], platformArtifacts: ["darwin-arm64"] });
  assert(result.ok, "Core Protocol sidecar builds without a manifest");
  if (!result.ok) return;
  assert(result.envelope.manifest === undefined, "protocol sidecar omits manifest");
  assert(result.envelope.sidecar?.localUrl === "http://127.0.0.1:7099", "loopback launch contract is present");
  assert(result.envelope.sidecar?.supervisedEnv === "BNBLOC_SAMPLECORE_SUPERVISED", "declared supervision env is included");
  assert(result.artifactRequirements.map((requirement) => requirement.platform).join(",") === "darwin-arm64", "declared platforms become artifact upload requirements");
}

function testSidecarCanIncludeOptionalLegacyManifest(): void {
  const result = buildComponentPackage({ kind: "bnbloc.core.package/v1", name: "legacy-sidecar", version: "1.0.0", publisher: "platform", runtime: "sidecar", protocol: { min: "1.0", max: "1.0" }, localUrl: "http://127.0.0.1:7099", platformArtifacts: ["darwin-arm64"], manifestText: "{\"name\":\"legacy-sidecar\"}" });
  assert(result.ok, "legacy sidecar manifest remains optional input");
  if (result.ok) assert(result.envelope.manifest !== undefined, "provided legacy manifest is included");
}

function testPluginSidecarCarriesHookContractAndArtifacts(): void {
  const result = buildComponentPackage({ kind: "bnbloc.plugin.package/v1", name: "audit-hook", version: "1.0.0", publisher: "platform", runtime: "sidecar", contract: "bnbloc.plugin.contract/v1", hooks: ["chat_message.v1"], localUrl: "http://127.0.0.1:7171", platformArtifacts: ["darwin-arm64"] });
  assert(result.ok, "plugin sidecar builds from its required contract fields");
  if (!result.ok) return;
  assert(result.envelope.hooks?.[0] === "chat_message.v1", "plugin hook is included");
  assert(result.envelope.protocol === undefined, "plugin package does not receive a core protocol field");
}

function testAuthProviderEndpointCarriesItsContract(): void {
  const result = buildComponentPackage({ kind: "bnbloc.auth-provider.package/v1", name: "sso-provider", version: "1.0.0", publisher: "platform", runtime: "endpoint", contract: "bnbloc.auth-provider.contract/v1", endpointUrl: "http://idp-bridge:8097", network: ["idp-bridge"] });
  assert(result.ok, "auth-provider endpoint package builds");
  if (!result.ok) return;
  assert(result.envelope.endpoint?.baseUrl === "http://idp-bridge:8097", "auth-provider endpoint URL is included");
  assert(result.envelope.contract === "bnbloc.auth-provider.contract/v1", "auth-provider contract is included");
}

function testRuntimeRestrictionsAndRequiredFields(): void {
  const noEndpoint = buildComponentPackage({ kind: "bnbloc.core.package/v1", name: "bad", version: "1.0.0", publisher: "p", runtime: "endpoint", protocol: { min: "1.0", max: "1.0" } });
  assert(!noEndpoint.ok && noEndpoint.error.toLowerCase().includes("endpoint"), "endpoint runtime requires a URL");
  const noPlatform = buildComponentPackage({ kind: "bnbloc.core.package/v1", name: "bad", version: "1.0.0", publisher: "p", runtime: "sidecar", protocol: { min: "1.0", max: "1.0" }, localUrl: "http://127.0.0.1:7099", platformArtifacts: [] });
  assert(!noPlatform.ok && noPlatform.error.toLowerCase().includes("platform"), "sidecar requires a selected binary platform");
}

function testFinalSignatureBytesContainUploadedArtifactMetadata(): void {
  const result = buildComponentPackage({ kind: "bnbloc.core.package/v1", name: "samplecore", version: "0.1.0", publisher: "platform", runtime: "sidecar", protocol: { min: "1.0", max: "1.0" }, localUrl: "http://127.0.0.1:7099", platformArtifacts: ["darwin-arm64"] });
  assert(result.ok, "sidecar draft builds");
  if (!result.ok) return;
  const final = buildFinalComponentPackageJSON(result.envelope, { "darwin-arm64": { url: "https://gateway.example.test/v1/artifacts/sha256/abc", sha256: "a".repeat(64) } });
  const parsed: unknown = JSON.parse(final);
  assert(typeof parsed === "object" && parsed !== null && "artifacts" in parsed && typeof parsed.artifacts === "object" && parsed.artifacts !== null, "final package contains artifact map");
  if (typeof parsed !== "object" || parsed === null || !("artifacts" in parsed) || typeof parsed.artifacts !== "object" || parsed.artifacts === null) return;
  const artifacts = parsed.artifacts;
  assert("darwin-arm64" in artifacts && typeof artifacts["darwin-arm64"] === "object" && artifacts["darwin-arm64"] !== null && "url" in artifacts["darwin-arm64"] && artifacts["darwin-arm64"].url === "https://gateway.example.test/v1/artifacts/sha256/abc", "final bytes include uploaded artifact URL");
  assert("darwin-arm64" in artifacts && typeof artifacts["darwin-arm64"] === "object" && artifacts["darwin-arm64"] !== null && "sha256" in artifacts["darwin-arm64"] && artifacts["darwin-arm64"].sha256 === "a".repeat(64), "final bytes include returned checksum");
  assert(!("manifest" in parsed), "final protocol sidecar bytes still omit manifest");
}

function testBuilderModesStayScopedToExecutablePackages(): void {
  const modes: readonly ComponentPackageBuildMode[] = [
    { kind: "bnbloc.core.package/v1", runtime: "endpoint", label: "Core endpoint", key: "bnbloc.core.package/v1:endpoint" },
    { kind: "bnbloc.core.package/v1", runtime: "manifest", label: "Core manifest", key: "bnbloc.core.package/v1:manifest" },
    { kind: "bnbloc.core.package/v1", runtime: "sidecar", label: "Core sidecar", key: "bnbloc.core.package/v1:sidecar" },
    { kind: "bnbloc.plugin.package/v1", runtime: "sidecar", label: "Plugin sidecar", key: "bnbloc.plugin.package/v1:sidecar" },
    { kind: "bnbloc.auth-provider.package/v1", runtime: "endpoint", label: "Auth-provider endpoint", key: "bnbloc.auth-provider.package/v1:endpoint" },
  ];
  const options = buildComponentPackageModes();
  assert(options.length === modes.length, "builder exposes supported kind/runtime pairs only");
  assert(options.map((option) => option.key).join(",") === modes.map((mode) => mode.key).join(","), "builder modes have stable package keys");
}

testEndpointCoreBuildsWithoutArtifacts();
testManifestCoreRequiresAndPreservesManifest();
testProtocolSidecarBuildsWithoutManifest();
testSidecarCanIncludeOptionalLegacyManifest();
testPluginSidecarCarriesHookContractAndArtifacts();
testAuthProviderEndpointCarriesItsContract();
testRuntimeRestrictionsAndRequiredFields();
testFinalSignatureBytesContainUploadedArtifactMetadata();
testBuilderModesStayScopedToExecutablePackages();
