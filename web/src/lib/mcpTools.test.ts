import type { MemberComponent } from "./apiTypes.ts";
import type { ContentComponent } from "./contentComponents.ts";
import { buildComponentPackage } from "./componentPackageBuilder.ts";
import { artifactRequirementsForPackage } from "./componentArtifactRequirements.ts";
import { parseComponentPackage } from "./componentPackage.ts";
import { nextPublishWizardStep, packageDraftFromEnvelope, previousPublishWizardStep } from "./componentPublishWizard.ts";
import type { DesktopComponentSettings } from "./componentSettings.ts";
import { credentialStatus, signInLine, signsIn, toolUsers } from "./mcpTools.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function equal(actual: unknown, expected: unknown): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
  }
}

function tool(mcp: MemberComponent["mcp"], extra: Partial<MemberComponent> = {}): MemberComponent {
  return { kind: "mcp", name: "codeword", version: "1.0.0", rollout: "auto", installed: true, managed: true, ...(mcp ? { mcp } : {}), ...extra };
}

const salt = { key: "salt", label: "Salt", type: "secret", scope: "member", required: true } as const;
const token = { key: "token", label: "API token", type: "secret", scope: "org", required: true } as const;
const member = tool({ tools: [{ name: "get_codeword" }], transport: "stdio" }, { settings: [salt] });
const local = (missing: string[]): DesktopComponentSettings => ({
  kind: "mcp", name: "codeword", version: "1.0.0", transport: "stdio", tools: [], auth: "", state: "ready", message: "", fields: [], missing,
});

test("an OAuth tool is signed in to, not configured", () => {
  const oauth = tool({ tools: [{ name: "list" }], transport: "http", http: { auth: { scheme: "oauth", scopes: ["files:read"] } } });
  equal(signsIn(oauth), true);
  equal(signsIn(member), false);
  equal(credentialStatus(oauth).state, "elsewhere");
  const signedIn = { ...local([]), auth: "oauth", signIn: { server: "auth.example.com", signedInAtMs: 1, expiresAtMs: 0, valid: true } };
  equal(credentialStatus(oauth, signedIn).text, "Signed in on this computer");
  equal(credentialStatus(oauth, { ...local([]), auth: "oauth" }).text, "Needs setup: sign in from Settings → Components");
  equal(signInLine(signedIn), "Signed in with auth.example.com.");
  equal(signInLine({ signIn: { ...signedIn.signIn, valid: false } }), "Your sign-in with auth.example.com expired. Sign in again.");
  equal(signInLine({}), "Not signed in. Sign in to use it in your runs.");
});

test("settings: none, an admin's and the member's own", () => {
  equal(credentialStatus(tool({ tools: [{ name: "t" }], transport: "http" })).state, "ready");
  equal(credentialStatus(tool({ tools: [], transport: "stdio" }, { settings: [token], settingsSet: ["token"] })).state, "ready");
  const unset = credentialStatus(tool({ tools: [], transport: "stdio" }, { settings: [token], settingsMissing: ["token"] }));
  equal(unset, { state: "missing", text: "Needs setup: an admin has not set API token" });
  // In a browser the member's own settings live on their desktop.
  equal(credentialStatus(member).state, "elsewhere");
  equal(credentialStatus(member, local([])).state, "ready");
  equal(credentialStatus(member, local(["salt"])).text, "Needs setup: set Salt in Settings → Components");
});

test("which experts and skill packs use a tool", () => {
  const c = (id: string, mcp: { name: string }[]) => ({ id, kind: "expert", requires: { mcp } }) as unknown as ContentComponent;
  const users = toolUsers("codeword", [c("a", [{ name: "codeword" }]), c("b", [{ name: "search" }]), c("c", [])]);
  equal(users.map((u) => u.id), ["a"]);
});

const stdioPackage = JSON.stringify({
  kind: "bnbloc.mcp.package/v1",
  name: "codeword",
  version: "1.0.0",
  publisher: "acme",
  contract: "bnbloc.mcp.contract/v1",
  runtime: "stdio",
  artifacts: { "darwin-arm64": { url: "", sha256: "" } },
  mcp: { tools: [{ name: "get_codeword" }], stdio: { args: ["serve"] } },
  settings: [{ key: "salt", label: "Salt", type: "secret", scope: "member", required: true, deliver: { env: "CODEWORD_SALT" } }],
});

test("the platform console imports an MCP package and uploads its binaries", () => {
  const parsed = parseComponentPackage(stdioPackage);
  if (!parsed.ok) throw new Error(parsed.error);
  equal(parsed.value.mcp?.stdio?.args, ["serve"]);
  equal(artifactRequirementsForPackage(parsed.value), [{ platform: "darwin-arm64" }]);
  equal(nextPublishWizardStep("configure", { runtime: "stdio" }), "artifacts");
  equal(previousPublishWizardStep("publish", "stdio"), "artifacts");
  // The configure step rebuilds the package from its draft: the mcp block
  // carries over unchanged.
  const draft = packageDraftFromEnvelope(parsed.value);
  const built = buildComponentPackage({ ...draft, platformArtifacts: ["darwin-arm64", "linux-amd64"] });
  if (!built.ok) throw new Error(built.error);
  equal(built.envelope.mcp, parsed.value.mcp);
  equal(built.envelope.settings, parsed.value.settings);
  equal(Object.keys(built.envelope.artifacts ?? {}).sort(), ["darwin-arm64", "linux-amd64"]);
  equal(built.envelope.sidecar, undefined);
});

test("the console refuses malformed MCP packages", () => {
  const bad = (patch: Record<string, unknown>) => parseComponentPackage(JSON.stringify({ ...JSON.parse(stdioPackage), ...patch }));
  equal(bad({ contract: "x" }).ok, false);
  equal(bad({ runtime: "sidecar" }).ok, false);
  equal(bad({ artifacts: undefined }).ok, false);
  equal(bad({ mcp: { stdio: {} } }).ok, false);
  const notMCP = parseComponentPackage(JSON.stringify({ kind: "bnbloc.notifier.package/v1", name: "n", version: "1.0.0", publisher: "p", runtime: "endpoint", contract: "bnbloc.notifier.contract/v1", endpoint: { baseUrl: "https://n.example" }, mcp: { tools: [] } }));
  equal(notMCP.ok, false);
});
