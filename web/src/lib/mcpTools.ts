// MCP tool components (docs/architecture/content-components.md, "MCP tool
// components"): platform-published tool servers experts and skill packs
// require by name. The member catalog (GET /v1/components?kind=mcp) lists
// the ones visible to the member; credentials come from the member's own
// desktop (Settings → Tools, never sent to the server) or from an org
// connector an org admin configures. Pure helpers; the calls are in
// mcpToolsApi.ts and lib/desktop.ts.
import type { ComponentMCPEnv, MemberComponent } from "./apiTypes.ts";
import type { ContentComponent } from "./contentComponents.ts";
import type { LocalSettingView } from "./pluginSettings.ts";

// McpToolSettings is one installed MCP tool as the desktop shell reports it
// (apps/desktop/src-tauri/src/internal_ext/mcp_tools.rs): the stdio binary's
// state on this computer and the member's settings (secret values never
// come back).
export interface McpToolSettings {
  tool: string;
  version: string;
  transport: string;
  credentials: string;
  tools: { name: string; description?: string }[];
  configured: boolean;
  state: string;
  message: string;
  fields: LocalSettingView[];
}

// One variable of an organization connector as an org admin sees it.
export interface McpConnectorField {
  readonly name: string;
  readonly description?: string;
  readonly secret?: boolean;
  readonly required?: boolean;
  readonly set: boolean;
  readonly value?: string;
}

export interface McpConnector {
  readonly name: string;
  readonly version: string;
  readonly description?: string;
  readonly transport: string;
  readonly configured: boolean;
  readonly fields: readonly McpConnectorField[];
  readonly updatedBy?: string;
  readonly updatedAtMs?: number;
}

export interface McpConnectorsReply {
  readonly available: boolean;
  readonly connectors: readonly McpConnector[];
}

export type CredentialState = "ready" | "missing" | "elsewhere";

export interface CredentialStatus {
  readonly state: CredentialState;
  readonly text: string;
}

// credentialStatus says whether a tool's credentials are in place for the
// member: none needed, the organization's connector, or the member's own
// on this desktop (local: the shell's report; undefined in a browser).
export function credentialStatus(tool: MemberComponent, local?: McpToolSettings): CredentialStatus {
  const mcp = tool.mcp;
  if (!mcp || !mcp.credentials || (mcp.env ?? []).length === 0) return { state: "ready", text: "No credentials needed" };
  if (mcp.credentials === "org") {
    return mcp.orgConfigured
      ? { state: "ready", text: "Configured by your organization" }
      : { state: "missing", text: "Missing: an organization admin has not configured it" };
  }
  if (!local) return { state: "elsewhere", text: "Your own credentials: set them in the desktop app (Settings → Tools)" };
  return local.configured
    ? { state: "ready", text: "Configured on this computer" }
    : { state: "missing", text: "Missing: set them in Settings → Tools" };
}

// requiredEnv lists a tool's variables as a short line ("TOKEN (secret) *").
export function envSummary(env: readonly ComponentMCPEnv[] | undefined): string {
  return (env ?? []).map((e) => `${e.name}${e.secret ? " (secret)" : ""}${e.required ? " *" : ""}`).join(", ");
}

// toolUsers returns the content components (experts, skill packs) that
// require a tool, by name.
export function toolUsers(tool: string, content: readonly ContentComponent[]): ContentComponent[] {
  return content.filter((c) => (c.requires.mcp ?? []).some((r) => r.name === tool));
}

// isMcpTool narrows a member catalog entry to an MCP tool.
export function isMcpTool(c: MemberComponent): boolean {
  return c.kind === "mcp";
}

// connectorFieldsAsSettings maps connector fields onto the local-settings
// form model, so the same form helpers (pluginSettings.ts) drive both.
export function connectorFieldsAsSettings(fields: readonly McpConnectorField[]): LocalSettingView[] {
  return fields.map((f) => ({
    key: f.name,
    label: f.name,
    ...(f.description ? { help: f.description } : {}),
    secret: !!f.secret,
    required: !!f.required,
    set: f.set,
    ...(f.value !== undefined ? { value: f.value } : {}),
  }));
}
