import { api, req, type MemberComponent } from "./api.ts";

// MCP tools in the member catalog (services/gateway/mcp_tools.go), in the
// tenant the session acts in (a super admin's acting tenant, lib/session.ts).
// Their settings go through componentSettingsApi.ts and lib/desktop.ts.
export const mcpToolsApi = {
  // The MCP tools visible to the caller (rollout, tenants and opt-in).
  list: () => req<{ components: MemberComponent[] }>("/v1/components?kind=mcp").then((r) => (r.components ?? []).filter((c) => c.kind === "mcp")),
  install: (name: string) => api.installComponent({ name, kind: "mcp" }),
  uninstall: (name: string) => api.uninstallComponent({ name, kind: "mcp" }),
};
