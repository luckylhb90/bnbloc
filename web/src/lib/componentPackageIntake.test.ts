import { inspectComponentFile } from "./componentPackageIntake.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const corePackage = JSON.stringify({ kind: "bnbloc.core.package/v1", name: "team-core", version: "1.4.0", publisher: "platform", runtime: "sidecar", protocol: { min: "1.0", max: "1.0" }, sidecar: { localUrl: "http://127.0.0.1:7099" }, artifacts: { "darwin-arm64": { url: "", sha256: "" } } });

async function testPackageEnvelopeIsDetectedByContentsNotFileName(): Promise<void> {
  const result = await inspectComponentFile(new File([corePackage], "download.data", { type: "application/json" }));
  assert(result.type === "package", "package JSON routes by envelope, even when filename is opaque");
  if (result.type === "package") assert(result.envelope.name === "team-core" && result.nextStep === "configure", "declared envelope determines next stage");
}

async function testUnsupportedDeclaredKindReceivesRecoverableContractError(): Promise<void> {
  const body = JSON.stringify({ kind: "acme-sso.integration/v1", config: {} });
  const result = await inspectComponentFile(new File([body], "identity.json", { type: "application/json" }));
  assert(result.type === "unsupported-package", "unknown declared kind remains a source step error");
  if (result.type === "unsupported-package") assert(result.message.includes("acme-sso.integration/v1") && result.message.includes("supported bnbloc component"), "error identifies unsupported contract without leaking parser phrasing");
}

async function testSamlMetadataIsRecognizedButNotPresentedAsRunnablePackage(): Promise<void> {
  const metadata = `<?xml version="1.0"?><md:EntityDescriptor xmlns:md="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example"><md:IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol" /></md:EntityDescriptor>`;
  const result = await inspectComponentFile(new File([metadata], "idp-metadata.xml", { type: "application/xml" }));
  assert(result.type === "external-metadata", "SAML metadata is recognized by standard XML structure");
  if (result.type === "external-metadata") assert(result.entityId === "https://idp.example" && result.message.includes("does not install or run"), "metadata guides to the missing auth-provider service, not package builder");
}

async function testPrivateKeyMaterialGetsProtectedConfigurationGuidance(): Promise<void> {
  const privateKey = "-----BEGIN PRIVATE KEY-----\nSENSITIVE-CONTENT\n-----END PRIVATE KEY-----";
  const result = await inspectComponentFile(new File([privateKey], "key.pem", { type: "application/x-pem-file" }));
  assert(result.type === "sensitive-credential", "private key material is never treated as package input");
  if (result.type === "sensitive-credential") assert(result.message.includes("protected secret configuration") && !result.message.includes("SENSITIVE-CONTENT"), "error guides to secret storage without echoing the key");
}

async function testElfMachineHeaderDeterminesPlatformButNotComponentKind(): Promise<void> {
  const elf = new Uint8Array(20); elf.set([0x7f, 0x45, 0x4c, 0x46, 2, 1]); elf[18] = 62;
  const result = await inspectComponentFile(new File([elf], "named-anything", { type: "application/octet-stream" }));
  assert(result.type === "binary", "ELF executable routes to a contract question");
  if (result.type === "binary") assert(result.detectedPlatform === "linux-amd64" && result.nextQuestion === "contract", "machine architecture is inferred from header, purpose is not guessed");
}

async function testProductNameDoesNotAffectBinaryRecognition(): Promise<void> {
  const elf = new Uint8Array(20); elf.set([0x7f, 0x45, 0x4c, 0x46, 2, 1]); elf[18] = 62;
  const result = await inspectComponentFile(new File([elf], "vendor-agentd-aarch64", { type: "application/octet-stream" }));
  assert(result.type === "binary", "product-named executable follows the same structural route");
  if (result.type === "binary") assert(result.detectedPlatform === "linux-amd64" && result.fileName === "vendor-agentd-aarch64", "header is the architecture source; filename is display-only");
}
async function testPeHeaderYieldsWindowsArchitectureWhenPresent(): Promise<void> {
  const pe = new Uint8Array(70);
  pe[0] = 0x4d; pe[1] = 0x5a;
  pe[0x3c] = 64;
  pe[64] = 0x50; pe[65] = 0x45;
  pe[68] = 0x64; pe[69] = 0x86;
  const result = await inspectComponentFile(new File([pe], "agent", { type: "application/octet-stream" }));
  assert(result.type === "binary" && result.detectedPlatform === "windows-amd64", "PE COFF header identifies Windows amd64 rather than a Linux architecture");
}
async function testInvalidJsonStaysOnSourceWithRetryGuidance(): Promise<void> {
  const result = await inspectComponentFile(new File(["{bad"], "package.json", { type: "application/json" }));
  assert(result.type === "invalid-json" && result.stage === "source", "invalid JSON stays on file-selection step");
  if (result.type === "invalid-json") assert(result.nextAction === "choose-another-file" && !result.message.includes("package kind must"), "retry uses a readable parse error");
}

async function testUnrelatedTextDoesNotExposePackageParserError(): Promise<void> {
  const result = await inspectComponentFile(new File(["hello"], "notes.txt", { type: "text/plain" }));
  assert(result.type === "unsupported-file" && result.stage === "source", "unrelated file stays in source step");
  if (result.type === "unsupported-file") assert(result.message.includes("could not identify") && result.recovery === "choose-json-or-binary", "error tells user how to recover");
}

async function run(): Promise<void> {
  await testPackageEnvelopeIsDetectedByContentsNotFileName();
  await testUnsupportedDeclaredKindReceivesRecoverableContractError();
  await testSamlMetadataIsRecognizedButNotPresentedAsRunnablePackage();
  await testPrivateKeyMaterialGetsProtectedConfigurationGuidance();
  await testElfMachineHeaderDeterminesPlatformButNotComponentKind();
  await testProductNameDoesNotAffectBinaryRecognition();
  await testPeHeaderYieldsWindowsArchitectureWhenPresent();
  await testInvalidJsonStaysOnSourceWithRetryGuidance();
  await testUnrelatedTextDoesNotExposePackageParserError();
}
void run();
