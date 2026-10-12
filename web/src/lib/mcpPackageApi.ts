import { ApiError } from "./api.ts";
import { apiUrl } from "./config.ts";
import { authedFetch, errorDetail } from "./session.ts";

// POST /v1/platform/mcp-packages (services/gateway/mcp_packages.go): the
// console's MCP tool draft in, the package JSON to sign out, as the exact
// text the gateway answered (signing covers these bytes).
export async function buildMCPPackage(body: Record<string, unknown>): Promise<string> {
  const res = await authedFetch(apiUrl("/v1/platform/mcp-packages"), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new ApiError(res.status, await errorDetail(res));
  return res.text();
}
