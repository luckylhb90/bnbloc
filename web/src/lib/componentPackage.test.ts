import { parseComponentPackage } from "./componentPackage.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const packageJSON = JSON.stringify({
  kind: "bnbloc.core.package/v1",
  name: "samplecore",
  version: "0.1.0",
  publisher: "platform",
  runtime: "sidecar",
  protocol: { min: "1.0", max: "1.0" },
  sidecar: { localUrl: "http://127.0.0.1:7099", listenEnv: "BNBLOC_SAMPLECORE_LISTEN", supervisedEnv: "BNBLOC_SAMPLECORE_SUPERVISED" },
  artifacts: { "darwin-arm64": { url: "", sha256: "" } },
});
const parsed = parseComponentPackage(packageJSON);
assert(parsed.ok, "protocol sidecar package imports without a manifest when it declares a platform placeholder");
if (parsed.ok) {
  assert(parsed.value.manifest === undefined, "imported Core Protocol package keeps manifest absent");
  assert(parsed.value.sidecar?.localUrl === "http://127.0.0.1:7099", "sidecar loopback launch contract survives package import");
  assert(parsed.value.sidecar?.supervisedEnv === "BNBLOC_SAMPLECORE_SUPERVISED", "sidecar supervision env survives package import");
}

const notifierJSON = JSON.stringify({
  kind: "bnbloc.notifier.package/v1",
  name: "corp-notify",
  version: "0.1.0",
  publisher: "platform",
  runtime: "endpoint",
  contract: "bnbloc.notifier.contract/v1",
  endpoint: { baseUrl: "http://notify.internal:8097/notifier" },
  permissions: { network: ["notify.internal"] },
});
const notifier = parseComponentPackage(notifierJSON);
assert(notifier.ok, "notifier endpoint package imports");
const notifierWithSettings = parseComponentPackage(JSON.stringify({ ...JSON.parse(notifierJSON), settings: [{ key: "a", label: "A", type: "text", scope: "org" }] }));
assert(!notifierWithSettings.ok, "settings are refused on notifier packages (slice 1: plugins and MCP tools)");
const wrongNotifier = parseComponentPackage(notifierJSON.replace("bnbloc.notifier.contract/v1", "bnbloc.notifier.contract/v9"));
assert(!wrongNotifier.ok, "an unknown notifier contract is refused");
const settingsOnCore = parseComponentPackage(JSON.stringify({ ...JSON.parse(packageJSON), settings: [{ key: "a", label: "A", type: "text", scope: "member" }] }));
assert(!settingsOnCore.ok, "settings are refused on core packages");
const malformedSettings = parseComponentPackage(JSON.stringify({ ...JSON.parse(packageJSON), kind: "bnbloc.plugin.package/v1", settings: [{ key: "a" }] }));
assert(!malformedSettings.ok, "a setting without label, type and scope is refused");
const hostOnCore = parseComponentPackage(JSON.stringify({ ...JSON.parse(packageJSON), permissions: { host: ["chat"] } }));
assert(!hostOnCore.ok, "permissions.host is refused on non-plugin packages");
const personalBot = parseComponentPackage(JSON.stringify({
  kind: "bnbloc.plugin.package/v1", name: "personal-bot", version: "0.1.0", publisher: "platform", runtime: "sidecar",
  contract: "bnbloc.plugin.contract/v1", hooks: ["notification.v1"],
  artifacts: { "darwin-arm64": { url: "https://gw.example/v1/artifacts/sha256/" + "a".repeat(64), sha256: "a".repeat(64) } },
  sidecar: { localUrl: "http://127.0.0.1:7193" },
  permissions: { network: ["127.0.0.1"], host: ["chat"] },
  settings: [
    { key: "appId", label: "App ID", type: "text", scope: "member", required: true },
    { key: "appKey", label: "App key", type: "secret", scope: "member", required: true },
  ],
}));
assert(personalBot.ok, personalBot.ok ? "" : personalBot.error);
if (personalBot.ok) {
  assert(personalBot.value.settings?.length === 2 && personalBot.value.settings[1]?.type === "secret", "the settings declaration survives package import");
  assert(personalBot.value.permissions?.host?.[0] === "chat", "host permissions survive package import");
}

const sandboxJSON = JSON.stringify({
  kind: "bnbloc.sandbox.package/v1",
  name: "sandboxd",
  version: "0.1.0",
  publisher: "platform",
  runtime: "sidecar",
  contract: "bnbloc.sandbox.contract/v1",
  protocol: { min: "1.0", max: "1.0" },
  capabilities: ["exec", "snapshots"],
  sidecar: {
    localUrl: "http://127.0.0.1:7311",
    tokenEnv: "SBX_TOKEN",
    dataDirEnv: "SBX_DATA_DIR",
    files: [{ name: "msb", env: "SBX_MSB_PATH", executable: true }],
  },
  artifacts: { "darwin-arm64": { url: "", sha256: "" }, "darwin-arm64/msb": { url: "", sha256: "" } },
});
const sandbox = parseComponentPackage(sandboxJSON);
assert(sandbox.ok, "sandbox sidecar package imports");
if (sandbox.ok) {
  assert(sandbox.value.sidecar?.tokenEnv === "SBX_TOKEN", "sandbox bearer env survives package import");
  assert(sandbox.value.sidecar?.files?.[0]?.executable === true, "sandbox sidecar files survive package import");
}
const tokenless = parseComponentPackage(sandboxJSON.replace('"tokenEnv":"SBX_TOKEN",', ""));
assert(!tokenless.ok, "a sandbox sidecar without tokenEnv is refused");
const wrongSandbox = parseComponentPackage(sandboxJSON.replace("bnbloc.sandbox.contract/v1", "bnbloc.sandbox.contract/v9"));
assert(!wrongSandbox.ok, "an unknown sandbox contract is refused");

const skillsPlugin = {
  kind: "bnbloc.plugin.package/v1",
  contract: "bnbloc.plugin.contract/v1",
  name: "local-skills",
  version: "0.1.0",
  publisher: "platform",
  runtime: "sidecar",
  capabilities: ["skills-provider", "sandbox-mounts"],
  sidecar: { localUrl: "http://127.0.0.1:7192", tokenEnv: "SK_TOKEN", mountsEnv: "SK_MOUNTS_FILE", mountRootEnv: "SK_MOUNT_ROOT" },
  artifacts: { "darwin-arm64": { url: "", sha256: "" } },
};
const skills = parseComponentPackage(JSON.stringify(skillsPlugin));
assert(skills.ok, "a plugin with another job needs no hook");
if (skills.ok) assert(skills.value.sidecar?.mountsEnv === "SK_MOUNTS_FILE", "mount envs survive package import");
assert(!parseComponentPackage(JSON.stringify({ ...skillsPlugin, capabilities: [] })).ok, "a plugin with no hook and no job is refused");
assert(!parseComponentPackage(JSON.stringify({ ...skillsPlugin, capabilities: ["skills-provider"] })).ok, "mount envs without the capability are refused");

const previewerJSON = JSON.stringify({
  kind: "bnbloc.previewer.package/v1",
  contract: "bnbloc.previewer.contract/v1",
  name: "sprite-sheet",
  version: "1.0.0",
  publisher: "examples",
  runtime: "bundle",
  artifacts: { bundle: { url: "", sha256: "" } },
  previewer: {
    entry: "index.html",
    match: { mimeTypes: ["image/png"], extensions: [".atlas.json"] },
    rank: 50,
    capabilities: ["thumbnail", "toolbar"],
    toolbar: [{ id: "grid", icon: "grid", label: "Grid", kind: "toggle" }],
  },
});
const previewer = parseComponentPackage(previewerJSON);
assert(previewer.ok, "previewer bundle package imports");
if (previewer.ok) {
  assert(previewer.value.previewer?.entry === "index.html", "previewer block survives package import (it is re-serialized at publish)");
  assert(previewer.value.previewer?.toolbar?.[0]?.id === "grid", "previewer toolbar survives package import");
}
assert(!parseComponentPackage(previewerJSON.replace('"runtime":"bundle"', '"runtime":"sidecar"')).ok, "a previewer must use the bundle runtime");
assert(!parseComponentPackage(previewerJSON.replace('"bundle":{"url"', '"other":{"url"')).ok, "a bundle runtime needs exactly the bundle artifact");
assert(!parseComponentPackage(previewerJSON.replace("bnbloc.previewer.package/v1", "bnbloc.plugin.package/v1")).ok, "previewer blocks are only valid on previewer packages");
