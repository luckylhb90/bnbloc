import { useCallback, useEffect, useState } from "react";
import { connectorFieldsAsSettings, type McpConnector } from "@/lib/mcpTools";
import { mcpToolsApi } from "@/lib/mcpToolsApi";
import { relativeTime } from "@/lib/format";
import { ErrorBox } from "@/ui/ErrorBox";
import { LocalSettingsCard } from "@/views/settings/LocalSettingsCard";
import { errorText } from "@/lib/errors";

// Org-admin: the organization's connectors for MCP tools with organization
// credentials (one per tool): the values every member's runs in this
// organization use. They are sealed on the server and handed only to the
// bridge running a job of this organization; secret values are never shown
// back. Tools with member credentials are set by each member on their own
// desktop instead.
export function ToolConnectorsTab() {
  const [connectors, setConnectors] = useState<readonly McpConnector[] | null>(null);
  const [available, setAvailable] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const reply = await mcpToolsApi.connectors();
      setAvailable(reply.available);
      setConnectors(reply.connectors ?? []);
    } catch (e) {
      setError(errorText(e, "failed to load tool connectors"));
      setConnectors([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <section className="flex flex-col gap-3 p-6">
      <div>
        <h2 className="text-sm font-semibold">Tool connectors</h2>
        <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
          Credentials for MCP tools your organization shares. Every member's runs in this organization use them; they are stored encrypted and never shown back.
        </p>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      {!available && (
        <p className="text-sm text-[var(--color-fg-subtle)]">
          Organization connectors are off on this deployment: the gateway has no secret store key (BNBLOC_SECRET_STORE_KEY). Ask your platform operator.
        </p>
      )}
      {connectors && connectors.length === 0 && !error && (
        <p className="text-sm text-[var(--color-fg-subtle)]">No MCP tool with organization credentials is published to this organization.</p>
      )}
      {available &&
        (connectors ?? []).map((c) => (
          <LocalSettingsCard
            key={c.name}
            title={c.name}
            version={c.version}
            status={
              c.configured
                ? `Configured${c.updatedBy ? ` by ${c.updatedBy}` : ""}${c.updatedAtMs ? ` ${relativeTime(c.updatedAtMs)}` : ""}. ${c.transport === "http" ? "An http tool" : "A stdio tool"}.`
                : "Not configured: members' runs show this tool as missing."
            }
            fields={connectorFieldsAsSettings(c.fields)}
            onSave={async (values) => {
              await mcpToolsApi.saveConnector(c.name, values);
              await load();
            }}
            onClear={async () => {
              await mcpToolsApi.clearConnector(c.name);
              await load();
            }}
            savedText={`${c.name} connector saved`}
            clearedText={`${c.name} connector removed`}
            clearLabel="Remove connector"
          >
            {c.description && <p className="text-xs text-[var(--color-fg-subtle)]">{c.description}</p>}
          </LocalSettingsCard>
        ))}
    </section>
  );
}
