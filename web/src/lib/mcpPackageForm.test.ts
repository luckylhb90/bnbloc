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
  settings: [
    { key: "salt", label: "Salt", type: "secret", scope: "member", required: true, help: "", deliver: "CODEWORD_SALT" },
    { key: "", label: "x", type: "text", scope: "org", required: false, help: "", deliver: "" },
  ],
  roomCredential: true,
  binaries: [{ platform: "darwin-arm64", key, fileName: "codeword" }],
  argsText: "--stdio\n\n  --verbose ",
};
const http: McpForm = {
  ...emptyMcpForm(),
  name: "search", version: "2.0.0", publisher: "acme", runtime: "endpoint",
  tools: [{ name: "search", description: "" }],
  settings: [
    { key: "token", label: "API token", type: "secret", scope: "org", required: true, help: "", deliver: "X-Ignored" },
    { key: "workspace", label: "Workspace", type: "text", scope: "member", required: false, help: "Yours.", deliver: "X-Workspace" },
  ],
  roomCredential: true, // stdio only: never sent for an http tool
  url: "https://tools.example.com/mcp", auth: "header", authHeader: "X-Api-Key", authSetting: "token",
};

test("network host from a URL", () => {
  equal(networkHost("https://tools.example.com:8443/mcp"), "tools.example.com");
  equal(networkHost("ftp://tools.example.com"), "");
  equal(networkHost("tools.example.com"), "");
});

test("a stdio draft: tools, settings delivered as env, the room credential, args, staged keys", () => {
  equal(mcpFormProblems(stdio), []);
  equal(mcpDraftBody(stdio), {
    name: "codeword", version: "1.0.0", publisher: "acme", runtime: "stdio",
    artifacts: { "darwin-arm64": { key } },
    mcp: {
      tools: [{ name: "get_codeword", description: "Answers the codeword." }],
      credentials: "room",
      stdio: { args: ["--stdio", "--verbose"] },
    },
    settings: [{ key: "salt", label: "Salt", type: "secret", scope: "member", required: true, deliver: { env: "CODEWORD_SALT" } }],
  });
});

test("an http draft: URL, header auth over a setting, other settings in headers, network from the URL", () => {
  equal(mcpFormProblems(http), []);
  equal(mcpDraftBody(http), {
    name: "search", version: "2.0.0", publisher: "acme", runtime: "endpoint",
    endpoint: { baseUrl: "https://tools.example.com/mcp" }, permissions: { network: ["tools.example.com"] },
    mcp: {
      tools: [{ name: "search" }],
      http: { auth: { scheme: "header", header: "X-Api-Key", setting: "token" } },
    },
    settings: [
      { key: "token", label: "API token", type: "secret", scope: "org", required: true },
      { key: "workspace", label: "Workspace", type: "text", scope: "member", help: "Yours.", deliver: { header: "X-Workspace" } },
    ],
  });
  // An edited network list wins over the URL's host.
  equal((mcpDraftBody({ ...http, networkText: "a.example.com\nb.example.com" }).permissions as { network: string[] }).network, ["a.example.com", "b.example.com"]);
  // No settings, auth none: no setting named, none declared.
  const open = mcpDraftBody({ ...http, settings: [], auth: "none" });
  equal([(open.mcp as Record<string, unknown>).http, open.settings], [{ auth: { scheme: "none" } }, undefined]);
});

test("problems in plain English", () => {
  equal(mcpFormProblems(emptyMcpForm()), ["Give the tool a name.", "Name the publisher.", "List at least one tool the server offers.", "Upload at least one platform binary."]);
  equal(mcpFormProblems({ ...stdio, binaries: [...stdio.binaries, { platform: "darwin-arm64", key, fileName: "b" }] }), ["Upload one binary per platform."]);
  equal(mcpFormProblems({ ...http, url: "nope", authSetting: "other", authHeader: "" }), [
    "Enter the server's http(s) URL.",
    "Pick the setting that holds the credential (add it under Settings).",
    "Name the header the credential goes in.",
  ]);
});

test("file name and the offline signing step", () => {
  equal(mcpPackageFileName("codeword", "1.0.0"), "codeword-1.0.0.package.json");
  equal(signSteps("codeword-1.0.0.package.json")[0], "bnbloc cores install --package codeword-1.0.0.package.json");
});
