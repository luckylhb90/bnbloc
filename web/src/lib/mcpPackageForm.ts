// The platform console's MCP tool form (docs/prd/2026-10-12-experts-and-
// teams.md, "Gap closure" 10). The form gathers the fields; the gateway
// (POST /v1/platform/mcp-packages) assembles the package with the assembly
// `bnbloc cores publish-mcp` uses and answers the JSON to sign. Nothing here
// re-implements the package rules: these helpers only shape the request and
// catch empty fields before a round trip. Everything here is pure.

// The platforms a binary is built for (goos-goarch), as the component
// wizard offers them.
export const BINARY_PLATFORMS = ["darwin-arm64", "darwin-amd64", "linux-amd64", "linux-arm64", "windows-amd64"] as const;

export type McpFormRuntime = "stdio" | "endpoint";
export type McpFormAuth = "none" | "bearer" | "header";

export interface McpFormTool {
  readonly name: string;
  readonly description: string;
}

export interface McpFormEnv {
  readonly name: string;
  readonly description: string;
  readonly secret: boolean;
  readonly required: boolean;
}

// McpFormBinary is one platform's staged binary (the key the artifact
// upload answered).
export interface McpFormBinary {
  readonly platform: string;
  readonly key: string;
  readonly fileName: string;
}

export interface McpForm {
  readonly name: string;
  readonly version: string;
  readonly publisher: string;
  readonly description: string;
  readonly runtime: McpFormRuntime;
  readonly tools: readonly McpFormTool[];
  readonly env: readonly McpFormEnv[];
  readonly credentials: "member" | "org";
  // stdio
  readonly binaries: readonly McpFormBinary[];
  readonly argsText: string; // one argument per line
  // http
  readonly url: string;
  readonly auth: McpFormAuth;
  readonly authHeader: string;
  readonly authEnv: string;
  readonly networkText: string; // one host per line
}

export function emptyMcpForm(): McpForm {
  return {
    name: "", version: "1.0.0", publisher: "", description: "", runtime: "stdio",
    tools: [{ name: "", description: "" }], env: [], credentials: "member",
    binaries: [], argsText: "", url: "", auth: "none", authHeader: "", authEnv: "", networkText: "",
  };
}

// networkHost is the host an http tool's URL reaches (its network
// permission), or "" for anything but an absolute http(s) URL.
export function networkHost(url: string): string {
  try {
    const u = new URL(url.trim());
    return u.protocol === "http:" || u.protocol === "https:" ? u.hostname : "";
  } catch {
    return "";
  }
}

function lines(text: string): string[] {
  return text.split(/\r?\n/).map((l) => l.trim()).filter((l) => l !== "");
}

// mcpFormProblems lists what the form still misses, in plain English. The
// gateway's validator stays the authority on everything else.
export function mcpFormProblems(f: McpForm): string[] {
  const out: string[] = [];
  if (!f.name.trim()) out.push("Give the tool a name.");
  if (!f.version.trim()) out.push("Give the tool a version.");
  if (!f.publisher.trim()) out.push("Name the publisher.");
  if (!f.tools.some((t) => t.name.trim())) out.push("List at least one tool the server offers.");
  const envNames = f.env.map((e) => e.name.trim()).filter((n) => n !== "");
  if (f.runtime === "stdio") {
    if (f.binaries.length === 0) out.push("Upload at least one platform binary.");
    const platforms = f.binaries.map((b) => b.platform);
    if (new Set(platforms).size !== platforms.length) out.push("Upload one binary per platform.");
  } else {
    if (!networkHost(f.url)) out.push("Enter the server's http(s) URL.");
    if (f.auth !== "none" && !envNames.includes(f.authEnv.trim())) out.push("Pick the variable that holds the credential (add it under Environment).");
    if (f.auth === "header" && !f.authHeader.trim()) out.push("Name the header the credential goes in.");
  }
  return out;
}

// mcpDraftBody is the request body of POST /v1/platform/mcp-packages.
export function mcpDraftBody(f: McpForm): Record<string, unknown> {
  const env = f.env
    .filter((e) => e.name.trim())
    .map((e) => ({ name: e.name.trim(), ...(e.description.trim() ? { description: e.description.trim() } : {}), ...(e.secret ? { secret: true } : {}), ...(e.required ? { required: true } : {}) }));
  const mcp: Record<string, unknown> = {
    tools: f.tools.filter((t) => t.name.trim()).map((t) => ({ name: t.name.trim(), ...(t.description.trim() ? { description: t.description.trim() } : {}) })),
    ...(env.length ? { env, credentials: f.credentials } : {}),
  };
  const body: Record<string, unknown> = {
    name: f.name.trim(), version: f.version.trim(), publisher: f.publisher.trim(),
    ...(f.description.trim() ? { description: f.description.trim() } : {}),
    runtime: f.runtime,
  };
  if (f.runtime === "stdio") {
    const args = lines(f.argsText);
    if (args.length) mcp.stdio = { args };
    body.artifacts = Object.fromEntries(f.binaries.map((b) => [b.platform, { key: b.key }]));
  } else {
    mcp.http = { auth: { scheme: f.auth, ...(f.auth === "header" ? { header: f.authHeader.trim() } : {}), ...(f.auth !== "none" ? { env: f.authEnv.trim() } : {}) } };
    body.endpoint = { baseUrl: f.url.trim() };
    const network = lines(f.networkText);
    const hosts = network.length ? network : [networkHost(f.url)].filter((h) => h !== "");
    if (hosts.length) body.permissions = { network: hosts };
  }
  body.mcp = mcp;
  return body;
}

export function mcpPackageFileName(name: string, version: string): string {
  return `${name.trim() || "mcp-tool"}-${version.trim() || "0.0.0"}.package.json`;
}

// signSteps are the offline steps after the console built the package: the
// console never signs.
export function signSteps(file: string): string[] {
  return [
    `bnbloc cores install --package ${file}`,
    `# signs with your publisher key (default ~/.bnbloc/component-publisher.key) and installs it`,
  ];
}
