import { req } from "./api.ts";

// POST /v1/platform/mcp-packages (services/gateway/mcp_packages.go): the
// console's MCP tool draft in, the package JSON to sign out, as the exact
// text the gateway answered (signing covers these bytes).
export async function buildMCPPackage(body: Record<string, unknown>): Promise<string> {
  return req<string>("/v1/platform/mcp-packages", { method: "POST", body: JSON.stringify(body), as: "text" });
}
