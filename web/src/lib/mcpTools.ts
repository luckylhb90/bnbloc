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

export type { MCPConnector as McpConnector, MCPConnectorField as McpConnectorField, MCPConnectorsReply as McpConnectorsReply } from "./wire.gen.ts";
import type { MCPConnectorField as McpConnectorField } from "./wire.gen.ts";

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
  // "oauth" when the member signs in to the tool ("" otherwise).
  auth: string;
  // The member's sign-in on this computer (oauth tools; never a token).
  signIn?: McpSignIn;
  state: string;
  message: string;
  fields: LocalSettingView[];
}

export interface McpSignIn {
  // The sign-in server's host.
  server: string;
  signedInAtMs: number;
  // 0 when the server stated no expiry.
  expiresAtMs: number;
  // Usable now, or refreshable.
  valid: boolean;
}

// signsIn reports a tool each member signs in to (OAuth) instead of
// entering credentials.
export function signsIn(tool: MemberComponent): boolean {
  return tool.mcp?.http?.auth.scheme === "oauth";
}

// signInLine is Settings → Tools' status of an OAuth tool on this computer.
export function signInLine(t: Pick<McpToolSettings, "signIn">): string {
  const s = t.signIn;
  if (!s) return "Not signed in. Sign in to use it in your runs.";
  if (!s.valid) return `Your sign-in${s.server ? ` with ${s.server}` : ""} expired. Sign in again.`;
  return `Signed in${s.server ? ` with ${s.server}` : ""}.`;
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
  if (mcp && signsIn(tool)) {
    if (!local) return { state: "elsewhere", text: "Your own sign-in: sign in from the desktop app (Settings → Tools)" };
    return local.configured
      ? { state: "ready", text: "Signed in on this computer" }
      : { state: "missing", text: "Missing: sign in from Settings → Tools" };
  }
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
