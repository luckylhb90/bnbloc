import type { MemberComponent } from "./apiTypes.ts";
import type { ContentComponent } from "./contentComponents.ts";
import { buildComponentPackage } from "./componentPackageBuilder.ts";
import { artifactRequirementsForPackage } from "./componentArtifactRequirements.ts";
import { parseComponentPackage } from "./componentPackage.ts";
import { nextPublishWizardStep, packageDraftFromEnvelope, previousPublishWizardStep } from "./componentPublishWizard.ts";
import { connectorFieldsAsSettings, credentialStatus, envSummary, signInLine, signsIn, toolUsers, type McpToolSettings } from "./mcpTools.ts";
import { initialValues, missingRequired, settingsUpdate } from "./pluginSettings.ts";

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

function tool(mcp: MemberComponent["mcp"]): MemberComponent {
  return { kind: "mcp", name: "codeword", version: "1.0.0", rollout: "auto", installed: true, managed: true, ...(mcp ? { mcp } : {}) };
}

const member = tool({ tools: [{ name: "get_codeword" }], env: [{ name: "SALT", secret: true, required: true }], credentials: "member", transport: "stdio" });
const local = (configured: boolean): McpToolSettings => ({
  tool: "codeword", version: "1.0.0", transport: "stdio", credentials: "member", tools: [], configured, auth: "", state: "ready", message: "", fields: [],
});

test("an OAuth tool is signed in to, not configured", () => {
  const oauth = tool({ tools: [{ name: "list" }], transport: "http", http: { auth: { scheme: "oauth", scopes: ["files:read"] } } });
  equal(signsIn(oauth), true);
  equal(signsIn(member), false);
  equal(credentialStatus(oauth).state, "elsewhere");
  const signedIn = { ...local(true), auth: "oauth", signIn: { server: "auth.example.com", signedInAtMs: 1, expiresAtMs: 0, valid: true } };
  equal(credentialStatus(oauth, signedIn).text, "Signed in on this computer");
  equal(credentialStatus(oauth, { ...local(false), auth: "oauth" }).text, "Missing: sign in from Settings → Tools");
  equal(signInLine(signedIn), "Signed in with auth.example.com.");
  equal(signInLine({ signIn: { ...signedIn.signIn, valid: false } }), "Your sign-in with auth.example.com expired. Sign in again.");
  equal(signInLine({}), "Not signed in. Sign in to use it in your runs.");
});

test("credentials: none, org and member", () => {
  equal(credentialStatus(tool({ tools: [{ name: "t" }], transport: "http" })).state, "ready");
  equal(credentialStatus(tool({ tools: [], env: [{ name: "T", required: true }], credentials: "org", orgConfigured: true })).state, "ready");
  equal(credentialStatus(tool({ tools: [], env: [{ name: "T", required: true }], credentials: "org", orgConfigured: false })).state, "missing");
  // In a browser the member's own credentials live on their desktop.
  equal(credentialStatus(member).state, "elsewhere");
  equal(credentialStatus(member, local(true)).state, "ready");
  equal(credentialStatus(member, local(false)).text, "Missing: set them in Settings → Tools");
});

test("which experts and skill packs use a tool", () => {
  const c = (id: string, mcp: { name: string }[]) => ({ id, kind: "expert", requires: { mcp } }) as unknown as ContentComponent;
  const users = toolUsers("codeword", [c("a", [{ name: "codeword" }]), c("b", [{ name: "search" }]), c("c", [])]);
  equal(users.map((u) => u.id), ["a"]);
});

test("env summary", () => {
  equal(envSummary([{ name: "SALT", secret: true, required: true }, { name: "LANG" }]), "SALT (secret) *, LANG");
  equal(envSummary(undefined), "");
});

test("connector fields drive the shared settings form", () => {
  const fields = connectorFieldsAsSettings([
    { name: "TOKEN", secret: true, required: true, set: true },
    { name: "REGION", description: "where", set: true, value: "eu" },
  ]);
  equal(initialValues(fields), { TOKEN: "", REGION: "eu" });
  // A stored secret counts as filled; an empty one keeps it on the server.
  equal(missingRequired(fields, { TOKEN: "", REGION: "eu" }), []);
  equal(settingsUpdate(fields, { TOKEN: "", REGION: " us " }), { TOKEN: "", REGION: "us" });
  equal(fields[1]?.help, "where");
});

const stdioPackage = JSON.stringify({
  kind: "bnbloc.mcp.package/v1",
  name: "codeword",
  version: "1.0.0",
  publisher: "acme",
  contract: "bnbloc.mcp.contract/v1",
  runtime: "stdio",
  artifacts: { "darwin-arm64": { url: "", sha256: "" } },
  mcp: { tools: [{ name: "get_codeword" }], env: [{ name: "SALT", secret: true, required: true }], credentials: "member", stdio: { args: ["serve"] } },
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
  equal(Object.keys(built.envelope.artifacts ?? {}).sort(), ["darwin-arm64", "linux-amd64"]);
  equal(built.envelope.sidecar, undefined);
});

test("the console refuses malformed MCP packages", () => {
  const bad = (patch: Record<string, unknown>) => parseComponentPackage(JSON.stringify({ ...JSON.parse(stdioPackage), ...patch }));
  equal(bad({ contract: "x" }).ok, false);
  equal(bad({ runtime: "sidecar" }).ok, false);
  equal(bad({ artifacts: undefined }).ok, false);
  equal(bad({ mcp: { env: [] } }).ok, false);
  const notMCP = parseComponentPackage(JSON.stringify({ kind: "bnbloc.notifier.package/v1", name: "n", version: "1.0.0", publisher: "p", runtime: "endpoint", contract: "bnbloc.notifier.contract/v1", endpoint: { baseUrl: "https://n.example" }, mcp: { tools: [] } }));
  equal(notMCP.ok, false);
});
