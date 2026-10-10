import type { ComponentPackageEnvelope, ComponentPreviewer, ComponentSidecarFile } from "./apiTypes";

type ParseResult =
  | { readonly ok: true; readonly value: ComponentPackageEnvelope }
  | { readonly ok: false; readonly error: string };

export function parseComponentPackage(raw: string): ParseResult {
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (caught: unknown) {
    return { ok: false, error: caught instanceof SyntaxError ? caught.message : "package is not valid JSON" };
  }
  if (!isRecord(parsed)) return { ok: false, error: "package must be a JSON object" };
  return parsePackageRecord(parsed);
}

function parsePackageRecord(record: Readonly<Record<string, unknown>>): ParseResult {
  const kind = readPackageKind(record["kind"]);
  if (!kind) return { ok: false, error: "package kind must be bnbloc.core.package/v1, bnbloc.plugin.package/v1, bnbloc.auth-provider.package/v1, bnbloc.notifier.package/v1, bnbloc.sandbox.package/v1, or bnbloc.previewer.package/v1" };
  const name = readRequiredString(record, "name");
  const version = readRequiredString(record, "version");
  const publisher = readRequiredString(record, "publisher");
  const runtime = readRuntime(record["runtime"]);
  if (!name || !version || !publisher || !runtime) return { ok: false, error: "package requires name, version, publisher, and runtime" };
  const protocol = readProtocol(record["protocol"]);
  const artifacts = readArtifacts(record["artifacts"]);
  if (!artifacts.ok) return artifacts;
  const sidecar = readSidecar(record["sidecar"]);
  if (!sidecar.ok) return sidecar;
  const hooks = readStringList(record["hooks"], "hooks");
  if (!hooks.ok) return hooks;
  const capabilities = readStringList(record["capabilities"], "capabilities");
  if (!capabilities.ok) return capabilities;
  const permissions = readPermissions(record["permissions"]);
  if (!permissions.ok) return permissions;
  const endpoint = readEndpoint(record["endpoint"]);
  const localSettings = readLocalSettings(record["localSettings"]);
  if (!localSettings.ok) return localSettings;
  if (localSettings.value && kind !== "bnbloc.plugin.package/v1") return { ok: false, error: "localSettings is only valid for plugin packages" };
  if (permissions.value?.host && kind !== "bnbloc.plugin.package/v1") return { ok: false, error: "permissions.host is only valid for plugin packages" };
  const previewer = readPreviewer(record["previewer"]);
  if (!previewer.ok) return previewer;
  if (previewer.value && kind !== "bnbloc.previewer.package/v1") return { ok: false, error: "previewer is only valid for previewer packages" };
  if (kind === "bnbloc.core.package/v1" && !protocol) return { ok: false, error: "core package requires protocol.min and protocol.max" };
  const shapeError = packageShapeError(kind, runtime, record, artifacts.value, sidecar.value, hooks.value);
  if (shapeError) return { ok: false, error: shapeError };
  return {
    ok: true,
    value: {
      kind,
      name,
      version,
      publisher,
      runtime,
      ...(typeof record["description"] === "string" ? { description: record["description"] } : {}),
      ...(protocol ? { protocol } : {}),
      ...(endpoint ? { endpoint } : {}),
      ...("manifest" in record ? { manifest: record["manifest"] } : {}),
      ...(sidecar.value ? { sidecar: sidecar.value } : {}),
      ...(artifacts.value ? { artifacts: artifacts.value } : {}),
      ...(capabilities.value ? { capabilities: capabilities.value } : {}),
      ...(permissions.value ? { permissions: permissions.value } : {}),
      ...(typeof record["contract"] === "string" ? { contract: record["contract"] } : {}),
      ...(hooks.value ? { hooks: hooks.value } : {}),
      ...(localSettings.value ? { localSettings: localSettings.value } : {}),
      ...(previewer.value ? { previewer: previewer.value } : {}),
    },
  };
}

function packageShapeError(
  kind: ComponentPackageEnvelope["kind"],
  runtime: ComponentPackageEnvelope["runtime"],
  record: Readonly<Record<string, unknown>>,
  artifacts: ComponentPackageEnvelope["artifacts"] | undefined,
  sidecar: ComponentPackageEnvelope["sidecar"] | undefined,
  hooks: readonly string[] | undefined,
): string | null {
  if (kind === "bnbloc.plugin.package/v1") {
    if (typeof record["contract"] !== "string") return "plugin package requires contract";
    if (record["contract"] !== "bnbloc.plugin.contract/v1") return `plugin package contract ${String(record["contract"])} is not supported`;
    if (runtime !== "sidecar") return "plugin package runtime must be sidecar";
    if (!hooks || hooks.length === 0) return "plugin package requires at least one hook";
  }
  if (kind === "bnbloc.auth-provider.package/v1") {
    if (typeof record["contract"] !== "string") return "auth-provider package requires contract";
    if (record["contract"] !== "bnbloc.auth-provider.contract/v1") return `auth-provider package contract ${String(record["contract"])} is not supported`;
    if (runtime !== "endpoint") return "auth-provider package runtime must be endpoint";
  }
  if (kind === "bnbloc.notifier.package/v1") {
    if (record["contract"] !== "bnbloc.notifier.contract/v1") return `notifier package contract ${String(record["contract"])} is not supported`;
    if (runtime !== "endpoint") return "notifier package runtime must be endpoint";
  }
  if (kind === "bnbloc.sandbox.package/v1") {
    if (record["contract"] !== "bnbloc.sandbox.contract/v1") return `sandbox package contract ${String(record["contract"])} is not supported`;
    if (runtime !== "sidecar" && runtime !== "endpoint") return "sandbox package runtime must be sidecar or endpoint";
    if (runtime === "sidecar" && !sidecar?.tokenEnv) return "sandbox sidecar requires sidecar.tokenEnv";
  }
  if (kind === "bnbloc.previewer.package/v1") {
    if (record["contract"] !== "bnbloc.previewer.contract/v1") return `previewer package contract ${String(record["contract"])} is not supported`;
    if (runtime !== "bundle") return "previewer package runtime must be bundle";
    if (!("previewer" in record)) return "previewer package requires previewer";
  }
  if (runtime === "bundle" && (Object.keys(artifacts ?? {}).length !== 1 || !artifacts?.["bundle"])) return "bundle runtime requires exactly one artifact named bundle";
  if (runtime === "endpoint" && !readEndpoint(record["endpoint"])) return "endpoint runtime requires endpoint.baseUrl";
  if (runtime === "manifest" && !("manifest" in record)) return "manifest runtime requires manifest";
  if (runtime === "endpoint" && artifacts) return "endpoint runtime must not declare artifacts";
  if (runtime === "manifest" && artifacts) return "manifest runtime must not declare artifacts";
	if (runtime === "sidecar" && (!sidecar || Object.keys(artifacts ?? {}).length === 0)) return "sidecar runtime requires sidecar.localUrl and artifacts";
  return null;
}

function readPackageKind(value: unknown): ComponentPackageEnvelope["kind"] | null {
  return value === "bnbloc.core.package/v1" ||
    value === "bnbloc.plugin.package/v1" ||
    value === "bnbloc.auth-provider.package/v1" ||
    value === "bnbloc.notifier.package/v1" ||
    value === "bnbloc.sandbox.package/v1" ||
    value === "bnbloc.previewer.package/v1"
    ? value
    : null;
}

function readRuntime(value: unknown): ComponentPackageEnvelope["runtime"] | null {
  return value === "endpoint" || value === "manifest" || value === "sidecar" || value === "bundle" ? value : null;
}

function readRequiredString(record: Readonly<Record<string, unknown>>, key: string): string | null {
  const value = record[key];
  return typeof value === "string" && value.trim() ? value : null;
}

function readEndpoint(value: unknown): ComponentPackageEnvelope["endpoint"] | null {
  if (!isRecord(value) || typeof value["baseUrl"] !== "string") return null;
  return { baseUrl: value["baseUrl"] };
}

function readProtocol(value: unknown): ComponentPackageEnvelope["protocol"] | null {
  if (!isRecord(value) || typeof value["min"] !== "string" || typeof value["max"] !== "string") return null;
  return { min: value["min"], max: value["max"] };
}

function readArtifacts(value: unknown): { readonly ok: true; readonly value?: ComponentPackageEnvelope["artifacts"] } | { readonly ok: false; readonly error: string } {
  if (value === undefined) return { ok: true };
  if (!isRecord(value)) return { ok: false, error: "artifacts must be a platform map" };
  const artifacts: Record<string, { readonly url: string; readonly sha256: string }> = {};
  for (const [platform, artifact] of Object.entries(value)) {
    if (!isRecord(artifact) || typeof artifact["url"] !== "string" || typeof artifact["sha256"] !== "string" || Object.keys(artifact).length !== 2) {
      return { ok: false, error: `artifacts.${platform} requires url and sha256 or omit that artifact entry before upload` };
    }
    artifacts[platform] = { url: artifact["url"], sha256: artifact["sha256"] };
  }
  return { ok: true, value: artifacts };
}

function readSidecar(value: unknown): { readonly ok: true; readonly value?: ComponentPackageEnvelope["sidecar"] } | { readonly ok: false; readonly error: string } {
  if (value === undefined) return { ok: true };
  if (!isRecord(value) || typeof value["localUrl"] !== "string") return { ok: false, error: "sidecar requires localUrl" };
  const args = readStringList(value["args"], "sidecar.args");
  if (!args.ok) return args;
  const files = readSidecarFiles(value["files"]);
  if (!files.ok) return files;
  return {
    ok: true,
    value: {
      localUrl: value["localUrl"],
      ...(typeof value["listenEnv"] === "string" ? { listenEnv: value["listenEnv"] } : {}),
      ...(typeof value["supervisedEnv"] === "string" ? { supervisedEnv: value["supervisedEnv"] } : {}),
      ...(typeof value["tokenEnv"] === "string" ? { tokenEnv: value["tokenEnv"] } : {}),
      ...(typeof value["dataDirEnv"] === "string" ? { dataDirEnv: value["dataDirEnv"] } : {}),
      ...(args.value ? { args: args.value } : {}),
      ...(files.value ? { files: files.value } : {}),
    },
  };
}

function readSidecarFiles(value: unknown): { readonly ok: true; readonly value?: readonly ComponentSidecarFile[] } | { readonly ok: false; readonly error: string } {
  if (value === undefined) return { ok: true };
  if (!Array.isArray(value)) return { ok: false, error: "sidecar.files must be an array" };
  const files: ComponentSidecarFile[] = [];
  for (const entry of value) {
    if (!isRecord(entry) || typeof entry["name"] !== "string" || typeof entry["env"] !== "string") return { ok: false, error: "sidecar.files entries require name and env" };
    files.push({ name: entry["name"], env: entry["env"], ...(entry["executable"] === true ? { executable: true } : {}) });
  }
  return { ok: true, value: files };
}

function readPermissions(value: unknown): { readonly ok: true; readonly value?: ComponentPackageEnvelope["permissions"] } | { readonly ok: false; readonly error: string } {
  if (value === undefined) return { ok: true };
  if (!isRecord(value)) return { ok: false, error: "permissions must be an object" };
  const network = readStringList(value["network"], "permissions.network");
  if (!network.ok) return network;
  const host = readStringList(value["host"], "permissions.host");
  if (!host.ok) return host;
  return { ok: true, value: { ...(network.value ? { network: network.value } : {}), ...(host.value ? { host: host.value } : {}) } };
}

function readLocalSettings(value: unknown): { readonly ok: true; readonly value?: ComponentPackageEnvelope["localSettings"] } | { readonly ok: false; readonly error: string } {
  if (value === undefined) return { ok: true };
  if (!Array.isArray(value)) return { ok: false, error: "localSettings must be a list" };
  const fields: NonNullable<ComponentPackageEnvelope["localSettings"]>[number][] = [];
  for (const item of value) {
    if (!isRecord(item) || typeof item["key"] !== "string" || typeof item["label"] !== "string") return { ok: false, error: "localSettings entries require key and label" };
    fields.push({
      key: item["key"],
      label: item["label"],
      ...(typeof item["help"] === "string" ? { help: item["help"] } : {}),
      ...(item["secret"] === true ? { secret: true } : {}),
      ...(item["required"] === true ? { required: true } : {}),
    });
  }
  return { ok: true, value: fields };
}

// readPreviewer keeps a previewer block's shape (the gateway validates its
// vocabularies and bounds at install; this only refuses what cannot be one).
function readPreviewer(value: unknown): { readonly ok: true; readonly value?: ComponentPreviewer } | { readonly ok: false; readonly error: string } {
  if (value === undefined) return { ok: true };
  if (!isRecord(value) || typeof value["entry"] !== "string" || typeof value["rank"] !== "number" || !isRecord(value["match"])) {
    return { ok: false, error: "previewer requires entry, match and rank" };
  }
  const match = value["match"];
  const mimeTypes = readStringList(match["mimeTypes"], "previewer.match.mimeTypes");
  if (!mimeTypes.ok) return mimeTypes;
  const extensions = readStringList(match["extensions"], "previewer.match.extensions");
  if (!extensions.ok) return extensions;
  const capabilities = readStringList(value["capabilities"], "previewer.capabilities");
  if (!capabilities.ok) return capabilities;
  const toolbar = value["toolbar"];
  if (toolbar !== undefined && (!Array.isArray(toolbar) || !toolbar.every((item) => isRecord(item) && ["id", "icon", "label", "kind"].every((k) => typeof item[k] === "string")))) {
    return { ok: false, error: "previewer.toolbar entries require id, icon, label and kind" };
  }
  return {
    ok: true,
    value: {
      entry: value["entry"],
      rank: value["rank"],
      match: { ...(mimeTypes.value ? { mimeTypes: mimeTypes.value } : {}), ...(extensions.value ? { extensions: extensions.value } : {}) },
      ...(capabilities.value ? { capabilities: capabilities.value } : {}),
      ...(Array.isArray(toolbar) ? { toolbar: toolbar as ComponentPreviewer["toolbar"] } : {}),
    },
  };
}

function readStringList(value: unknown, label: string): { readonly ok: true; readonly value?: readonly string[] } | { readonly ok: false; readonly error: string } {
  if (value === undefined) return { ok: true };
  if (!Array.isArray(value) || !value.every((item) => typeof item === "string")) return { ok: false, error: `${label} must be a string list` };
  return { ok: true, value };
}

function isRecord(value: unknown): value is Readonly<Record<string, unknown>> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
