import { parseComponentPackage } from "./componentPackage.ts";
import type { ComponentPackageEnvelope } from "./apiTypes";

export type InspectedComponentFile =
  | { readonly type: "package"; readonly envelope: ComponentPackageEnvelope; readonly nextStep: "configure" }
  | { readonly type: "unsupported-package"; readonly message: string; readonly recovery: "component-envelope" }
  | { readonly type: "invalid-json"; readonly stage: "source"; readonly message: string; readonly nextAction: "choose-another-file" }
  | { readonly type: "binary"; readonly fileName: string; readonly nextQuestion: "contract"; readonly detectedPlatform?: string }
  | { readonly type: "external-metadata"; readonly entityId?: string; readonly message: string; readonly nextAction: "auth-provider-service" }
  | { readonly type: "sensitive-credential"; readonly message: string }
  | { readonly type: "unsupported-file"; readonly stage: "source"; readonly message: string; readonly recovery: "choose-json-or-binary" };

type ExecutablePlatform = "linux-amd64" | "linux-arm64" | "linux-386" | "linux-arm" | "darwin-arm64" | "darwin-amd64" | "windows-amd64" | "windows-arm64";
const SNIFF_LIMIT = 64 * 1024;
const MAX_PACKAGE_JSON = 16 * 1024 * 1024;

export async function inspectComponentFile(file: File): Promise<InspectedComponentFile> {
  const prefix = new Uint8Array(await file.slice(0, SNIFF_LIMIT).arrayBuffer());
  const prefixText = new TextDecoder("utf-8", { fatal: false }).decode(prefix);

  if (/-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/.test(prefixText)) {
    return { type: "sensitive-credential", message: "This looks like private key material. Do not upload it here; use the provider’s protected secret configuration." };
  }

  if (/<\s*(?:[\w.-]+:)?EntityDescriptor\b/.test(prefixText)
    && /urn:oasis:names:tc:SAML:2\.0:metadata/.test(prefixText)
    && /<\s*(?:[\w.-]+:)?IDPSSODescriptor\b/.test(prefixText)) {
    const entityId = prefixText.match(/\bentityID\s*=\s*["']([^"']+)["']/)?.[1];
    return {
      type: "external-metadata",
      ...(entityId ? { entityId } : {}),
      message: "This is SAML Identity Provider metadata. It configures an external IdP; it does not install or run a bnbloc auth-provider. A service implementing bnbloc.auth-provider.contract/v1 must be deployed first.",
      nextAction: "auth-provider-service",
    };
  }

  const jsonLike = /(?:application|text)\/(?:json|[^;]+\+json)/i.test(file.type)
    || /\.(?:json|bnbloc-component)$/i.test(file.name)
    || prefixText.trimStart().startsWith("{");
  if (jsonLike) {
    if (file.size > MAX_PACKAGE_JSON) {
      return { type: "unsupported-file", stage: "source", message: "This JSON is larger than the package inspection limit. Choose a supported package file.", recovery: "choose-json-or-binary" };
    }
    const text = await file.text();
    const parsed = parseComponentPackage(text);
    if (parsed.ok) return { type: "package", envelope: parsed.value, nextStep: "configure" };

    let value: unknown;
    try { value = JSON.parse(text) as unknown; }
    catch (cause: unknown) {
      return { type: "invalid-json", stage: "source", message: cause instanceof SyntaxError ? `This JSON file could not be read: ${cause.message}` : "This file is not valid JSON.", nextAction: "choose-another-file" };
    }
    if (typeof value === "object" && value !== null && "kind" in value) {
      const kind = typeof value.kind === "string" ? value.kind : "an invalid component kind";
      return { type: "unsupported-package", message: `This file declares “${kind}”, which is not a supported bnbloc component package contract.`, recovery: "component-envelope" };
    }
    return { type: "unsupported-package", message: "This is valid JSON, but it does not declare a bnbloc component package contract. Provider settings alone do not install a runnable auth-provider service.", recovery: "component-envelope" };
  }

  const platform = executablePlatform(prefix);
  if (platform !== undefined || isRecognizedExecutable(prefix)) {
    return { type: "binary", fileName: file.name, nextQuestion: "contract", ...(platform ? { detectedPlatform: platform } : {}) };
  }
  return { type: "unsupported-file", stage: "source", message: "We could not identify this file as a bnbloc package, executable, or SAML IdP metadata. Choose a supported file or a component contract.", recovery: "choose-json-or-binary" };
}

function isRecognizedExecutable(prefix: Uint8Array): boolean {
  const elf = prefix[0] === 0x7f && prefix[1] === 0x45 && prefix[2] === 0x4c && prefix[3] === 0x46;
  const pe = prefix[0] === 0x4d && prefix[1] === 0x5a && prefix[0x3c] !== undefined;
  const macho = prefix[0] === 0xcf && prefix[1] === 0xfa && prefix[2] === 0xed && prefix[3] === 0xfe
    || prefix[0] === 0xfe && prefix[1] === 0xed && prefix[2] === 0xfa && prefix[3] === 0xcf
    || prefix[0] === 0xca && prefix[1] === 0xfe && prefix[2] === 0xba && prefix[3] === 0xbe;
  return elf || pe || macho;
}

function executablePlatform(prefix: Uint8Array): ExecutablePlatform | undefined {
  if (prefix[0] === 0x7f && prefix[1] === 0x45 && prefix[2] === 0x4c && prefix[3] === 0x46) {
    if ((prefix[4] !== 1 && prefix[4] !== 2) || (prefix[5] !== 1 && prefix[5] !== 2)) return undefined;
    const machine = prefix[5] === 1 ? prefix[18] | (prefix[19] << 8) : prefix[19] | (prefix[18] << 8);
    if (machine === 62 && prefix[4] === 2) return "linux-amd64";
    if (machine === 183 && prefix[4] === 2) return "linux-arm64";
    if (machine === 3 && prefix[4] === 1) return "linux-386";
    if (machine === 40 && prefix[4] === 1) return "linux-arm";
    return undefined;
  }

  if (prefix[0] === 0x4d && prefix[1] === 0x5a) {
    const peOffset = prefix[0x3c] | (prefix[0x3d] << 8) | (prefix[0x3e] << 16) | (prefix[0x3f] << 24);
    if (prefix[peOffset] !== 0x50 || prefix[peOffset + 1] !== 0x45 || prefix[peOffset + 2] !== 0 || prefix[peOffset + 3] !== 0) return undefined;
    const machine = prefix[peOffset + 4] | (prefix[peOffset + 5] << 8);
    return machine === 0x8664 ? "windows-amd64" : machine === 0xaa64 ? "windows-arm64" : undefined;
  }

  const littleMachO = prefix[0] === 0xcf && prefix[1] === 0xfa && prefix[2] === 0xed && prefix[3] === 0xfe;
  const bigMachO = prefix[0] === 0xfe && prefix[1] === 0xed && prefix[2] === 0xfa && prefix[3] === 0xcf;
  if (littleMachO || bigMachO) {
    const machine = littleMachO
      ? prefix[4] | (prefix[5] << 8) | (prefix[6] << 16) | (prefix[7] << 24)
      : (prefix[4] << 24) | (prefix[5] << 16) | (prefix[6] << 8) | prefix[7];
    if ((machine & 0x00ffffff) === 0x0000000c) return "darwin-arm64";
    if ((machine & 0x00ffffff) === 0x00000007) return "darwin-amd64";
  }
  return undefined;
}
