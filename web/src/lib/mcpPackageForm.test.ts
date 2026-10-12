import { emptyMcpForm, mcpDraftBody, mcpFormProblems, mcpPackageFileName, networkHost, signSteps, type McpForm } from "./mcpPackageForm.ts";

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

const key = "sha256/" + "a".repeat(64);
const stdio: McpForm = {
  ...emptyMcpForm(),
  name: " codeword ", publisher: "acme",
  tools: [{ name: "get_codeword", description: "Answers the codeword." }, { name: " ", description: "" }],
  env: [{ name: "CODEWORD_SALT", description: "", secret: true, required: true }, { name: "", description: "x", secret: false, required: false }],
  binaries: [{ platform: "darwin-arm64", key, fileName: "codeword" }],
  argsText: "--stdio\n\n  --verbose ",
};
const http: McpForm = {
  ...emptyMcpForm(),
  name: "search", version: "2.0.0", publisher: "acme", runtime: "endpoint",
  tools: [{ name: "search", description: "" }],
  env: [{ name: "TOOLS_TOKEN", description: "API token", secret: true, required: true }], credentials: "org",
  url: "https://tools.example.com/mcp", auth: "header", authHeader: "X-Api-Key", authEnv: "TOOLS_TOKEN",
};

test("network host from a URL", () => {
  equal(networkHost("https://tools.example.com:8443/mcp"), "tools.example.com");
  equal(networkHost("ftp://tools.example.com"), "");
  equal(networkHost("tools.example.com"), "");
});

test("a stdio draft: tools, env with credentials, args, staged keys", () => {
  equal(mcpFormProblems(stdio), []);
  equal(mcpDraftBody(stdio), {
    name: "codeword", version: "1.0.0", publisher: "acme", runtime: "stdio",
    artifacts: { "darwin-arm64": { key } },
    mcp: {
      tools: [{ name: "get_codeword", description: "Answers the codeword." }],
      env: [{ name: "CODEWORD_SALT", secret: true, required: true }], credentials: "member",
      stdio: { args: ["--stdio", "--verbose"] },
    },
  });
});

test("an http draft: URL, header auth, network from the URL", () => {
  equal(mcpFormProblems(http), []);
  equal(mcpDraftBody(http), {
    name: "search", version: "2.0.0", publisher: "acme", runtime: "endpoint",
    endpoint: { baseUrl: "https://tools.example.com/mcp" }, permissions: { network: ["tools.example.com"] },
    mcp: {
      tools: [{ name: "search" }], env: [{ name: "TOOLS_TOKEN", description: "API token", secret: true, required: true }], credentials: "org",
      http: { auth: { scheme: "header", header: "X-Api-Key", env: "TOOLS_TOKEN" } },
    },
  });
  // An edited network list wins over the URL's host.
  equal((mcpDraftBody({ ...http, networkText: "a.example.com\nb.example.com" }).permissions as { network: string[] }).network, ["a.example.com", "b.example.com"]);
  // No env: no credentials source, auth none carries no env.
  const open = mcpDraftBody({ ...http, env: [], auth: "none" }).mcp as Record<string, unknown>;
  equal([open.credentials, open.http], [undefined, { auth: { scheme: "none" } }]);
});

test("problems in plain English", () => {
  equal(mcpFormProblems(emptyMcpForm()), ["Give the tool a name.", "Name the publisher.", "List at least one tool the server offers.", "Upload at least one platform binary."]);
  equal(mcpFormProblems({ ...stdio, binaries: [...stdio.binaries, { platform: "darwin-arm64", key, fileName: "b" }] }), ["Upload one binary per platform."]);
  equal(mcpFormProblems({ ...http, url: "nope", authEnv: "OTHER", authHeader: "" }), [
    "Enter the server's http(s) URL.",
    "Pick the variable that holds the credential (add it under Environment).",
    "Name the header the credential goes in.",
  ]);
});

test("file name and the offline signing step", () => {
  equal(mcpPackageFileName("codeword", "1.0.0"), "codeword-1.0.0.package.json");
  equal(signSteps("codeword-1.0.0.package.json")[0], "bnbloc cores install --package codeword-1.0.0.package.json");
});
