import type { MemberComponent } from "./apiTypes.ts";
import type { ContentComponent } from "./contentComponents.ts";
import {
  addMcpTool,
  clearPromptRef,
  mcpPermission,
  packPromptOptions,
  pickPromptRef,
  removeMcpTool,
  settingChoices,
  toggleSetting,
  urlHost,
  withMcp,
} from "./contentFormRefs.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function pack(name: string, version: string, mine: boolean, prompts: { id: string; title: string }[]): ContentComponent {
  return {
    id: "cc_" + name + (mine ? "_mine" : ""),
    kind: "prompt",
    name,
    displayName: name === "release" ? "Release notes" : "",
    latestVersion: version,
    mine,
    requires: {},
    prompts: prompts.map((p) => ({ ...p, body: "b", uses: 0 })),
  } as unknown as ContentComponent;
}

// Quick prompt refs: options, picking adds the requirement, keeps an
// existing range, clearing returns to text.
{
  const opts = packPromptOptions([
    pack("release", "2.3.1", false, [{ id: "notes", title: "Write notes" }]),
    pack("release", "1.0.0", true, [{ id: "draft", title: "Draft" }]),
    pack("triage", "0.4.0", false, [{ id: "bug", title: "Bug report" }]),
  ]);
  eq(
    opts.map((o) => [o.ref, o.label, o.range]),
    [
      ["release/draft", "Release notes · Draft", "^1"],
      ["triage/bug", "triage · Bug report", "^0"],
    ],
    "one pack per name, the member's own first",
  );
  const quick = [{ title: "Ship", prompt: "old text" }, { title: "Other", prompt: "x" }];
  const picked = pickPromptRef(quick, { skills: ["lint@^1"] }, 0, opts[0]!);
  eq(picked.quickPrompts[0], { title: "Ship", prompt: "", ref: "release/draft" }, "the ref replaces the text");
  eq(picked.quickPrompts[1], quick[1], "other quick prompts unchanged");
  eq(picked.requires, { skills: ["lint@^1"], prompts: ["release@^1"] }, "the pack is required at its major");
  const kept = pickPromptRef(quick, { prompts: ["release@~1.0"] }, 1, opts[0]!);
  eq(kept.requires.prompts, ["release@~1.0"], "an existing requirement keeps its range");
  const bare = pickPromptRef(quick, { prompts: ["release"] }, 1, opts[0]!);
  eq(bare.requires.prompts, ["release"], "a bare-name requirement counts too");
  eq(clearPromptRef(picked.quickPrompts, 0)[0], { title: "Ship", prompt: "" }, "clearing drops the ref");
}

function tool(name: string, http: string | null, settings: { key: string; required?: boolean }[] = []): MemberComponent {
  return {
    kind: "mcp",
    name,
    version: "1.0.0",
    rollout: "auto",
    installed: true,
    managed: true,
    runtime: http ? "endpoint" : "stdio",
    ...(http ? { endpoint: { baseUrl: http } } : {}),
    mcp: { tools: [], transport: http ? "http" : "stdio" },
    settings: settings.map((x) => ({ ...x, label: x.key, type: "text", scope: "org" })),
  } as unknown as MemberComponent;
}

// MCP permissions follow the selected tools; other permissions stay.
{
  eq(urlHost("https://User@Tools.Example.com:8443/mcp?x=1"), "tools.example.com:8443", "host with port, no userinfo");
  eq(urlHost("http://tools.example.com"), "tools.example.com", "host without path");
  const code = tool("codeword", null, [{ key: "salt", required: true }, { key: "region" }, { key: "debug" }]);
  const search = tool("search", "https://Search.example.com/mcp");
  const tools = [code, search];
  eq(mcpPermission(code), "mcp.stdio", "stdio permission");
  eq(mcpPermission(search), "mcp.http:search.example.com", "http permission");

  let req = addMcpTool({ permissions: ["scripts"] }, code, tools);
  eq(req, { permissions: ["scripts", "mcp.stdio"], mcp: [{ name: "codeword" }] }, "a stdio tool adds mcp.stdio");
  req = addMcpTool(req, search, tools);
  eq(req.permissions, ["scripts", "mcp.stdio", "mcp.http:search.example.com"], "an http tool adds its host");
  eq(addMcpTool(req, search, tools), req, "adding twice is a no-op");
  req = removeMcpTool(req, "codeword", tools);
  eq(req.permissions, ["scripts", "mcp.http:search.example.com"], "removing the last stdio tool drops mcp.stdio");
  req = removeMcpTool(req, "search", tools);
  eq(req, { permissions: ["scripts"], mcp: [] }, "removing every tool keeps only scripts");

  // A tool no longer visible keeps the declared MCP permissions.
  const stale = withMcp({ mcp: [{ name: "gone" }], permissions: ["mcp.http:old.example.com"] }, [{ name: "gone" }, { name: "codeword" }], tools);
  eq(stale.permissions, ["mcp.http:old.example.com", "mcp.stdio"], "an unknown tool keeps declared MCP permissions");
}

// Settings: required always needed, optional ticked, unknown keys dropped.
{
  const code = tool("codeword", null, [{ key: "salt", required: true }, { key: "region" }, { key: "debug" }]);
  let req = { mcp: [{ name: "codeword", settings: ["debug", "old"] }] };
  eq(
    settingChoices(code, req.mcp[0]!).map((c) => [c.setting.key, c.checked, c.locked]),
    [
      ["salt", true, true],
      ["region", false, false],
      ["debug", true, false],
    ],
    "required locked on, listed optional on",
  );
  req = toggleSetting(req, code, "region", true) as typeof req;
  eq(req.mcp[0], { name: "codeword", settings: ["region", "debug"] }, "declared order, unknown dropped");
  eq(toggleSetting(req, code, "salt", false), req, "a required setting cannot be unticked");
  req = toggleSetting(toggleSetting(req, code, "region", false), code, "debug", false) as typeof req;
  eq(req.mcp[0], { name: "codeword" }, "no optional settings: no list");
}
