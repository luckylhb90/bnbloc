import { api, req, scoped, type MemberComponent } from "./api.ts";
import type { McpConnectorsReply } from "./mcpTools.ts";

const enc = encodeURIComponent;

// MCP tools in the member catalog, and the organization connectors org
// admins configure (services/gateway/mcp_tools.go). tenantId scopes a super
// admin's connector calls to the organization they administer.
export const mcpToolsApi = {
  // The MCP tools visible to the caller (rollout, tenants and opt-in).
  list: () => req<{ components: MemberComponent[] }>("/v1/components?kind=mcp").then((r) => (r.components ?? []).filter((c) => c.kind === "mcp")),
  install: (name: string) => api.installComponent({ name, kind: "mcp" }),
  uninstall: (name: string) => api.uninstallComponent({ name, kind: "mcp" }),
  connectors: (tenantId?: string) => req<McpConnectorsReply>(scoped("/v1/tenant/mcp-connectors", tenantId)),
  // Store the organization's values (an empty secret keeps the stored one).
  saveConnector: (name: string, values: Record<string, string>, tenantId?: string) =>
    req<void>(scoped(`/v1/tenant/mcp-connectors/${enc(name)}`, tenantId), { method: "PUT", body: JSON.stringify({ values }), raw: true }),
  clearConnector: (name: string, tenantId?: string) =>
    req<void>(scoped(`/v1/tenant/mcp-connectors/${enc(name)}`, tenantId), { method: "DELETE", raw: true }),
};
