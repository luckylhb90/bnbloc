import { useCallback, useEffect, useState } from "react";
import { desktop } from "@/lib/desktop";
import type { McpToolSettings } from "@/lib/mcpTools";
import { ErrorBox } from "@/ui/ErrorBox";
import { LocalSettingsCard } from "@/views/settings/LocalSettingsCard";

// ToolSettings (Settings → Tools) holds the member's own credentials for
// the MCP tools experts and skill packs use, and shows whether each stdio
// tool is downloaded on this computer. Credentials stay on this computer
// (0600 under the app's config dir), are never sent to the server, and are
// used only for runs you ask for yourself: someone borrowing your agent
// never gets them. Tools with organization credentials are configured by an
// org admin. In a browser there is nothing to set.
export function ToolSettings() {
  const inShell = desktop.available();
  const [tools, setTools] = useState<McpToolSettings[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      setTools((await desktop.mcpToolSettings()) ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load tool settings");
      setTools([]);
    }
  }, []);

  useEffect(() => {
    if (inShell) void load();
  }, [inShell, load]);

  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold">Tools</h2>
        <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
          Credentials for the MCP tools your experts and skills use. They stay on this computer and are used only for runs you ask for yourself.
        </p>
      </div>
      {!inShell && <p className="text-sm text-[var(--color-fg-subtle)]">Tool credentials live in the bnbloc desktop app. Open Settings → Tools there.</p>}
      {inShell && error && <ErrorBox>{error}</ErrorBox>}
      {inShell && tools && tools.length === 0 && !error && (
        <p className="text-sm text-[var(--color-fg-subtle)]">No MCP tools are installed for you. They are listed in Library → Tools once your platform admin publishes them.</p>
      )}
      {(tools ?? []).map((t) => (
        <LocalSettingsCard
          key={t.tool}
          title={t.tool}
          version={t.version}
          status={toolStatus(t)}
          fields={t.fields}
          onSave={async (values) => {
            await desktop.saveMcpToolSettings(t.tool, values);
            await load();
          }}
          onClear={async () => {
            await desktop.clearMcpToolSettings(t.tool);
            await load();
          }}
          savedText={`${t.tool} credentials saved on this computer`}
          clearedText={`${t.tool} credentials removed from this computer`}
        >
          {t.tools.length > 0 && <p className="text-xs text-[var(--color-fg-subtle)]">Tools: {t.tools.map((x) => x.name).join(", ")}</p>}
        </LocalSettingsCard>
      ))}
    </section>
  );
}

// toolStatus is one line: where the credentials come from and, for a stdio
// tool, whether its binary is on this computer.
function toolStatus(t: McpToolSettings): string {
  const creds =
    t.credentials === "org"
      ? "Credentials: configured by your organization."
      : t.fields.length === 0
        ? "No credentials needed."
        : t.configured
          ? "Credentials set on this computer."
          : "Not set up yet: fill in the fields below.";
  if (t.transport !== "stdio") return `${creds} Runs at its service URL.`;
  const local = t.state === "ready" ? "Downloaded and verified." : t.state === "failed" ? `Download failed: ${t.message}` : t.message || "Downloading…";
  return `${creds} ${local}`;
}
