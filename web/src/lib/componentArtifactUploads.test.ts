import type { ComponentPackageEnvelope } from "./apiTypes";
import {
  artifactRequirementsForPackage,
  buildFinalComponentPackageJSON,
  buildPackageWithUploadedArtifacts,
  uploadedArtifactsComplete,
} from "./componentArtifactRequirements.ts";
import { parseComponentPackage } from "./componentPackage.ts";

const sidecarCorePackage: ComponentPackageEnvelope = {
  kind: "bnbloc.core.package/v1",
  name: "samplecore",
  version: "1.2.3",
  publisher: "platform",
  runtime: "sidecar",
  protocol: { min: "1.0", max: "1.0" },
  sidecar: { localUrl: "http://127.0.0.1:7099" },
  artifacts: {
    "darwin-arm64": { url: "https://example.invalid/old", sha256: "a".repeat(64) },
    "linux-amd64": { url: "https://example.invalid/old-linux", sha256: "b".repeat(64) },
  },
};

const authProviderPackage: ComponentPackageEnvelope = {
  kind: "bnbloc.auth-provider.package/v1",
  name: "sso-provider",
  version: "0.1.0",
  publisher: "platform",
  runtime: "endpoint",
  contract: "bnbloc.auth-provider.contract/v1",
  endpoint: { baseUrl: "http://127.0.0.1:8097" },
};

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function testArtifactRequirements(): void {
  const requirements = artifactRequirementsForPackage(sidecarCorePackage);
  assert(requirements.length === 2, "sidecar packages require one upload per declared platform");
  assert(requirements[0]?.platform === "darwin-arm64", "platforms stay sorted for stable UI");
  assert(!uploadedArtifactsComplete(requirements, {}), "publish is blocked before uploads complete");
  assert(
    !uploadedArtifactsComplete(requirements, {
      "darwin-arm64": { url: "", sha256: "" },
      "linux-amd64": { url: "", sha256: "" },
    }),
    "explicit upload placeholders do not unlock publish",
  );
  assert(
    uploadedArtifactsComplete(requirements, {
      "darwin-arm64": { url: "http://localhost:7090/v1/artifacts/sha256/a", sha256: "c".repeat(64) },
      "linux-amd64": { url: "http://localhost:7090/v1/artifacts/sha256/b", sha256: "d".repeat(64) },
    }),
    "publish unlocks when every declared platform has uploaded metadata",
  );
}

function testBuildPackageWithUploadedArtifacts(): void {
  const next = buildPackageWithUploadedArtifacts(sidecarCorePackage, {
    "darwin-arm64": { url: "http://localhost:7090/v1/artifacts/sha256/1", sha256: "1".repeat(64) },
    "linux-amd64": { url: "http://localhost:7090/v1/artifacts/sha256/2", sha256: "2".repeat(64) },
  });
  assert(next.artifacts?.["darwin-arm64"]?.url === "http://localhost:7090/v1/artifacts/sha256/1", "uses the uploaded artifact URL");
  assert(next.artifacts?.["linux-amd64"]?.sha256 === "2".repeat(64), "uses the uploaded artifact sha256");
  assert(sidecarCorePackage.artifacts?.["darwin-arm64"]?.url === "https://example.invalid/old", "does not mutate parsed package input");
}

function testFinalPackageJSONUsesUploadedArtifactsWithoutMutatingInput(): void {
  const finalJSON = buildFinalComponentPackageJSON(sidecarCorePackage, {
    "darwin-arm64": { url: "http://localhost:7090/v1/artifacts/sha256/1", sha256: "1".repeat(64) },
    "linux-amd64": { url: "http://localhost:7090/v1/artifacts/sha256/2", sha256: "2".repeat(64) },
  });
  assert(finalJSON.includes("/v1/artifacts/sha256/1"), "final package JSON contains uploaded artifact URL");
  assert(finalJSON === buildFinalComponentPackageJSON(sidecarCorePackage, {
    "darwin-arm64": { url: "http://localhost:7090/v1/artifacts/sha256/1", sha256: "1".repeat(64) },
    "linux-amd64": { url: "http://localhost:7090/v1/artifacts/sha256/2", sha256: "2".repeat(64) },
  }), "final package bytes are deterministic for signature tooling");
  assert(!finalJSON.includes("example.invalid/old"), "final package JSON excludes pre-upload placeholder URLs");
  assert(sidecarCorePackage.artifacts?.["darwin-arm64"]?.url === "https://example.invalid/old", "helper does not mutate parsed package input after signature review");
}

function testParsePackageAcceptsOnlyExplicitUploadPlaceholder(): void {
  const explicit = JSON.stringify({
    ...sidecarCorePackage,
    artifacts: { "darwin-arm64": { url: "", sha256: "" } },
  });
  const parsedExplicit = parseComponentPackage(explicit);
  assert(parsedExplicit.ok, "explicit empty url+sha256 placeholder is accepted before upload");

  const garbage = JSON.stringify({
    ...sidecarCorePackage,
    artifacts: { "darwin-arm64": { garbage: "value" } },
  });
  const parsedGarbage = parseComponentPackage(garbage);
  assert(!parsedGarbage.ok && parsedGarbage.error.includes("artifacts.darwin-arm64 requires url and sha256 or omit that artifact entry"), "garbage artifact object is rejected");

  const partial = JSON.stringify({
    ...sidecarCorePackage,
    artifacts: { "darwin-arm64": { url: "http://localhost:7090/v1/artifacts/sha256/1" } },
  });
  const parsedPartial = parseComponentPackage(partial);
  assert(!parsedPartial.ok && parsedPartial.error.includes("artifacts.darwin-arm64 requires url and sha256 or omit that artifact entry"), "partial artifact object is rejected");
}

function testParsePackageKeepsEndpointAndManifestJsonOnly(): void {
  const endpointPackage: ComponentPackageEnvelope = {
    kind: "bnbloc.core.package/v1",
    name: "remote-core",
    version: "1.0.0",
    publisher: "platform",
    runtime: "endpoint",
    protocol: { min: "1.0", max: "1.0" },
    endpoint: { baseUrl: "https://core.example" },
    artifacts: { "darwin-arm64": { url: "", sha256: "" } },
  };
  const endpointParsed = parseComponentPackage(JSON.stringify(endpointPackage));
  assert(!endpointParsed.ok && endpointParsed.error.includes("endpoint runtime must not declare artifacts"), "endpoint packages cannot carry upload placeholders");

  const manifestPackage: ComponentPackageEnvelope = {
    kind: "bnbloc.core.package/v1",
    name: "manifest-core",
    version: "1.0.0",
    publisher: "platform",
    runtime: "manifest",
    protocol: { min: "1.0", max: "1.0" },
    manifest: { name: "manifest-core" },
    artifacts: { "darwin-arm64": { url: "", sha256: "" } },
  };
  const manifestParsed = parseComponentPackage(JSON.stringify(manifestPackage));
  assert(!manifestParsed.ok && manifestParsed.error.includes("manifest runtime must not declare artifacts"), "manifest packages cannot carry upload placeholders");
}

function testEndpointPackagesRemainJsonOnly(): void {
  const endpointPackage: ComponentPackageEnvelope = {
    kind: "bnbloc.core.package/v1",
    name: "remote-core",
    version: "1.0.0",
    publisher: "platform",
    runtime: "endpoint",
    protocol: { min: "1.0", max: "1.0" },
    endpoint: { baseUrl: "https://core.example" },
  };
  assert(artifactRequirementsForPackage(endpointPackage).length === 0, "endpoint packages do not ask for binary uploads");
  assert(buildPackageWithUploadedArtifacts(endpointPackage, {}) === endpointPackage, "JSON-only packages keep their parsed envelope");
}

function testAuthProviderPackageParsesAsEndpointComponent(): void {
  const parsed = parseComponentPackage(JSON.stringify(authProviderPackage));
  assert(parsed.ok, "auth-provider endpoint packages parse in the generic component console");
  if (!parsed.ok) return;
  assert(parsed.value.kind === "bnbloc.auth-provider.package/v1", "auth-provider package kind is preserved");
  assert(parsed.value.contract === "bnbloc.auth-provider.contract/v1", "auth-provider contract is preserved");
  assert(artifactRequirementsForPackage(parsed.value).length === 0, "auth-provider endpoints do not ask for binary uploads");

  const unsupported = parseComponentPackage(JSON.stringify({ ...authProviderPackage, contract: "bnbloc.auth-provider.contract/v9" }));
  assert(!unsupported.ok && unsupported.error.includes("auth-provider package contract"), "unknown auth-provider contracts are rejected before upload");
}

testArtifactRequirements();
testBuildPackageWithUploadedArtifacts();
testFinalPackageJSONUsesUploadedArtifactsWithoutMutatingInput();
testParsePackageAcceptsOnlyExplicitUploadPlaceholder();
testParsePackageKeepsEndpointAndManifestJsonOnly();
testEndpointPackagesRemainJsonOnly();
testAuthProviderPackageParsesAsEndpointComponent();
