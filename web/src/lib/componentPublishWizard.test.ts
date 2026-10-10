import type { ComponentPackageEnvelope } from "./apiTypes.ts";
import { createPackageDraft, nextPublishWizardStep, packageDraftFromEnvelope, previousPublishWizardStep } from "./componentPublishWizard.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function testForwardTransitionsGateRequiredStages(): void {
  assert(nextPublishWizardStep("source", { sourceReady: false }) === null, "source cannot advance before choosing a task");
  assert(nextPublishWizardStep("source", { sourceReady: true }) === "configure", "chosen task advances to configuration");
  assert(nextPublishWizardStep("configure", { runtime: "endpoint" }) === "publish", "endpoint packages skip binary upload");
  assert(nextPublishWizardStep("configure", { runtime: "sidecar" }) === "artifacts", "sidecars require the artifact stage");
  assert(nextPublishWizardStep("artifacts", { artifactsReady: false }) === null, "sidecar cannot continue before all binaries upload");
  assert(nextPublishWizardStep("artifacts", { artifactsReady: true }) === "publish", "uploaded sidecar advances to signing and publish");
}

function testBackTransitionsReturnToThePreviousTask(): void {
  assert(previousPublishWizardStep("configure", "endpoint") === "source", "configuration backs up to source selection");
  assert(previousPublishWizardStep("artifacts", "sidecar") === "configure", "artifact selection backs up to package configuration");
  assert(previousPublishWizardStep("publish", "sidecar") === "artifacts", "sidecar review backs up to artifact upload");
  assert(previousPublishWizardStep("publish", "endpoint") === "configure", "endpoint review backs up to configuration");
}

function testBlankCoreDraftDoesNotChooseAnUnverifiedTarget(): void {
  const draft = createPackageDraft("bnbloc.core.package/v1", "sidecar");
  assert(draft.platformArtifacts?.length === 0, "new binary packages require an explicit platform choice instead of an invisible default");
}

function testImportedPackagePrefillsItsOwnContractFields(): void {
  const envelope: ComponentPackageEnvelope = { kind: "bnbloc.auth-provider.package/v1", name: "identity", version: "2.3.0", publisher: "security", runtime: "endpoint", endpoint: { baseUrl: "https://auth.example" }, contract: "bnbloc.auth-provider.contract/v1", permissions: { network: ["auth.example"] } };
  const draft = packageDraftFromEnvelope(envelope);
  assert(draft.kind === envelope.kind && draft.runtime === envelope.runtime, "package declaration survives content-based import");
  assert(draft.endpointUrl === "https://auth.example" && draft.network?.[0] === "auth.example", "endpoint and network details are inferred from the actual envelope");
}


function testImportedPackagePrefillsEditableDraft(): void {
  const envelope: ComponentPackageEnvelope = {
    kind: "bnbloc.plugin.package/v1", name: "room-audit", version: "2.3.0", publisher: "security-team", description: "Room event audit", runtime: "sidecar",
    sidecar: { localUrl: "http://127.0.0.1:7171", listenEnv: "AUDIT_LISTEN", args: ["serve"] },
    artifacts: { "linux-amd64": { url: "https://gateway.example/audit", sha256: "a".repeat(64) } },
    permissions: { network: ["audit.internal"] }, contract: "bnbloc.plugin.contract/v1", hooks: ["chat_message.v1"], capabilities: ["room-events"],
  };
  const draft = packageDraftFromEnvelope(envelope);
  assert(draft.kind === envelope.kind && draft.runtime === envelope.runtime, "import retains component kind and runtime");
  assert(draft.name === "room-audit" && draft.version === "2.3.0" && draft.publisher === "security-team", "import prefills identity");
  assert(draft.localUrl === "http://127.0.0.1:7171" && draft.listenEnv === "AUDIT_LISTEN", "import prefills sidecar launch settings");
  assert(draft.platformArtifacts?.join(",") === "linux-amd64", "import retains declared target platforms");
  assert(draft.network?.[0] === "audit.internal" && draft.hooks?.[0] === "chat_message.v1", "import prefills declared permissions and hooks");
}

function testUserSelectedSidecarContractStartsWithoutFilenameDefaults(): void {
  const draft = createPackageDraft("bnbloc.core.package/v1", "sidecar");
  assert(draft.name === "" && draft.localUrl === undefined && draft.listenEnv === undefined, "empty core sidecar draft contains no guessed identity or listener settings");
  assert(draft.platformArtifacts?.length === 0, "platform must be selected or read from a proven executable header");
}

testForwardTransitionsGateRequiredStages();
testBackTransitionsReturnToThePreviousTask();
testBlankCoreDraftDoesNotChooseAnUnverifiedTarget();
testImportedPackagePrefillsItsOwnContractFields();
testImportedPackagePrefillsEditableDraft();
testUserSelectedSidecarContractStartsWithoutFilenameDefaults();
