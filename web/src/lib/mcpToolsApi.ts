import { api, req, type MemberComponent } from "./api.ts";
import type { McpConnectorsReply } from "./mcpTools.ts";

const enc = encodeURIComponent;

// MCP tools in the member catalog, and the organization connectors org
// admins configure (services/gateway/mcp_tools.go), in the tenant the session
// acts in (a super admin's acting tenant, lib/session.ts).
export const mcpToolsApi = {
  // The MCP tools visible to the caller (rollout, tenants and opt-in).
  list: () => req<{ components: MemberComponent[] }>("/v1/components?kind=mcp").then((r) => (r.components ?? []).filter((c) => c.kind === "mcp")),
  install: (name: string) => api.installComponent({ name, kind: "mcp" }),
  uninstall: (name: string) => api.uninstallComponent({ name, kind: "mcp" }),
  connectors: () => req<McpConnectorsReply>("/v1/tenant/mcp-connectors"),
  // Store the organization's values (an empty secret keeps the stored one).
  saveConnector: (name: string, values: Record<string, string>) =>
    req<void>(`/v1/tenant/mcp-connectors/${enc(name)}`, { method: "PUT", body: JSON.stringify({ values }), raw: true }),
  clearConnector: (name: string) =>
    req<void>(`/v1/tenant/mcp-connectors/${enc(name)}`, { method: "DELETE", raw: true }),
};
