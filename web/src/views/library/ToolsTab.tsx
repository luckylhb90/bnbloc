import { useCallback, useEffect, useMemo, useState } from "react";
import { MagnifyingGlassIcon, WrenchIcon } from "@phosphor-icons/react";
import type { MemberComponent } from "@/lib/api";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { desktop } from "@/lib/desktop";
import { settingsSummary, type DesktopComponentSettings } from "@/lib/componentSettings";
import { credentialStatus, toolUsers } from "@/lib/mcpTools";
import { mcpToolsApi } from "@/lib/mcpToolsApi";
import { Button } from "@/ui/Button";
import { Badge, Card, Skeleton } from "@/ui/Card";
import { Input } from "@/ui/Input";
import { EmptyState, PageHeader } from "@/ui/Page";
import { useToast } from "@/ui/Toast";
import { errorText } from "@/lib/errors";
import { LoadFailed } from "@/ui/QueryState";

// ToolsTab is the Library's MCP tools: the tool servers published by the
// platform, as the member may use them (rollout, tenants, opt-in). A tool
// that is on for the member reaches every conversation they start, with
// any agent; experts and skill packs can also require one
// (docs/prd/2026-10-17-browser-component.md §3.6). Each card shows the
// tools it offers, whether it is set up for the member (its settings: their
// own on this desktop, the organization's and the platform's) and which
// experts and skill packs use it. A member sets their own in Settings →
// Components; an org admin the organization's in Organization → Component
// settings.
export function ToolsTab({ tabs }: { readonly tabs: React.ReactNode }) {
  const toast = useToast();
  const [tools, setTools] = useState<MemberComponent[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [content, setContent] = useState<ContentComponent[]>([]);
  const [local, setLocal] = useState<Map<string, DesktopComponentSettings> | null>(null);
  const [q, setQ] = useState("");
  const [epoch, setEpoch] = useState(0);
  const reload = useCallback(() => setEpoch((n) => n + 1), []);

  useEffect(() => {
    let live = true;
    setTools(null);
    setLoadError(null);
    Promise.all([mcpToolsApi.list(), contentApi.list("expert"), contentApi.list("skill"), desktop.componentSettings().catch(() => null)])
      .then(([list, experts, skills, settings]) => {
        if (!live) return;
        setTools(list);
        setContent([...(experts.components ?? []), ...(skills.components ?? [])]);
        setLocal(settings ? new Map(settings.filter((s) => s.kind === "mcp").map((s) => [s.name, s])) : null);
      })
      .catch((e) => live && setLoadError(errorText(e, "Could not load the tools")));
    return () => {
      live = false;
    };
  }, [epoch]);

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (tools ?? []).filter(
      (t) => !needle || t.name.includes(needle) || (t.description ?? "").toLowerCase().includes(needle) || (t.mcp?.tools ?? []).some((x) => x.name.toLowerCase().includes(needle)),
    );
  }, [tools, q]);

  const toggle = async (t: MemberComponent, on: boolean) => {
    try {
      if (on) await mcpToolsApi.install(t.name);
      else await mcpToolsApi.uninstall(t.name);
      toast.success(on ? `${t.name} is on for you` : `${t.name} is off for you`);
      reload();
    } catch (e) {
      toast.error(errorText(e, "Could not change it"));
    }
  };

  return (
    <>
      <PageHeader title="Library" subtitle="Tool servers experts and skills can use, published by your platform" actions={tabs} />
      <div className="p-6">
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <div className="relative min-w-[16rem] max-w-lg flex-1">
            <MagnifyingGlassIcon size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
            <Input className="pl-9" placeholder="Search tools…" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
        </div>
        {loadError ? (
          <LoadFailed message={loadError} onRetry={reload} />
        ) : tools === null ? (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-40" />
            ))}
          </div>
        ) : shown.length === 0 ? (
          <EmptyState
            icon={<WrenchIcon size={26} />}
            title={q ? "Nothing matches" : "No tools yet"}
            hint={q ? undefined : "MCP tools are published by your platform admin. A tool that is on for you reaches every conversation you start, with any agent."}
          />
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {shown.map((t) => {
              const cred = credentialStatus(t, local?.get(t.name));
              const users = toolUsers(t.name, content);
              const vars = settingsSummary(t.settings);
              const download = local?.get(t.name);
              return (
                <Card key={t.name} className="flex min-w-0 flex-col gap-2 p-4">
                  <div className="flex min-w-0 items-start gap-2">
                    <div className="min-w-0 flex-1">
                      <h3 className="truncate font-medium">{t.name}</h3>
                      <p className="truncate font-mono text-xs text-[var(--color-fg-subtle)]">
                        v{t.version} · {t.mcp?.transport === "http" ? "http" : "stdio"}
                        {t.publisher ? ` · ${t.publisher}` : ""}
                      </p>
                    </div>
                    <Badge tone={cred.state === "ready" ? "accent" : cred.state === "missing" ? "warning" : "neutral"}>
                      {cred.state === "ready" ? "Ready" : cred.state === "missing" ? "Needs setup" : "Own settings"}
                    </Badge>
                  </div>
                  {t.description && <p className="line-clamp-2 text-sm text-[var(--color-fg-muted)]">{t.description}</p>}
                  {(t.mcp?.tools.length ?? 0) > 0 && (
                    <div className="flex flex-wrap gap-1">
                      {t.mcp?.tools.slice(0, 8).map((x) => (
                        <Badge key={x.name} title={x.description}>
                          {x.name}
                        </Badge>
                      ))}
                      {(t.mcp?.tools.length ?? 0) > 8 && <Badge>+{(t.mcp?.tools.length ?? 0) - 8}</Badge>}
                    </div>
                  )}
                  <p className="text-xs text-[var(--color-fg-muted)]">{cred.text}</p>
                  {vars && <p className="truncate text-xs text-[var(--color-fg-subtle)]" title={vars}>Settings: {vars}</p>}
                  {download && t.mcp?.transport !== "http" && (
                    <p className="text-xs text-[var(--color-fg-subtle)]">{download.state === "ready" ? "Downloaded on this computer" : download.message || "Downloading on this computer…"}</p>
                  )}
                  <p className="text-xs text-[var(--color-fg-subtle)]">
                    {t.installed ? "Your agents get it in the conversations you start" : "Turn it on to give it to your agents"}
                    {users.length > 0 && ` · Used by ${users.map(contentLabel).join(", ")}`}
                  </p>
                  {t.rollout === "optin" && (
                    <div className="mt-auto pt-1">
                      <Button size="sm" variant={t.installed ? "ghost" : "primary"} onClick={() => void toggle(t, !t.installed)}>
                        {t.installed ? "Turn off for me" : "Turn on for me"}
                      </Button>
                    </div>
                  )}
                </Card>
              );
            })}
          </div>
        )}
      </div>
    </>
  );
}
