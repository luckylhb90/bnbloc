import { useEffect, useState } from "react";
import { DownloadSimpleIcon, ShieldWarningIcon, SparkleIcon } from "@phosphor-icons/react";
import {
  contentLabel,
  formatSize,
  isTeam,
  permissionLabel,
  usageLine,
  type ContentComponent,
  type ContentComponentDetail,
  type ContentVersionDetail,
} from "@/lib/contentComponents";
import { apiErrorText, contentApi } from "@/lib/contentComponentsApi";
import { relativeTime } from "@/lib/format";
import { saveBlob } from "@/lib/saveBlob";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { Dialog } from "@/ui/Dialog";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { ContentByline } from "@/views/library/ContentSharing";
import { TeamBadge, TeamRoster } from "@/views/library/ContentFormExtras";

// PermissionBadges shows what a component declares (scripts, local tools,
// hosts it calls); someone else's script-bearing content runs sandboxed.
export function PermissionBadges({ component }: { readonly component: ContentComponent }) {
  if (component.permissions.length === 0) return null;
  return (
    <span className="flex flex-wrap items-center gap-1">
      {component.permissions.map((p) => (
        <Badge key={p} tone="warning" title={p}>
          <ShieldWarningIcon size={12} />
          {permissionLabel(p)}
        </Badge>
      ))}
    </span>
  );
}

// ContentViewDialog shows one component: a version's instructions, skills
// and files, the version list, and bundle downloads. roomId reads a
// component someone else pinned in that room. onImprove offers "Improve
// with AI" on the member's own components.
export function ContentViewDialog({
  component,
  roomId,
  onClose,
  onImprove,
}: {
  readonly component: ContentComponent | null;
  readonly roomId?: string;
  readonly onClose: () => void;
  readonly onImprove?: (c: ContentComponent) => void;
}) {
  const toast = useToast();
  const [detail, setDetail] = useState<ContentComponentDetail | null>(null);
  const [version, setVersion] = useState("");
  const [view, setView] = useState<ContentVersionDetail | null>(null);

  useEffect(() => {
    setDetail(null);
    setView(null);
    if (!component) return;
    setVersion(component.version || component.latestVersion);
    let live = true;
    contentApi
      .get(component.id, roomId)
      .then((d) => live && setDetail(d))
      .catch((e) => live && toast.error(apiErrorText(e, "Could not load it")));
    return () => {
      live = false;
    };
  }, [component, roomId, toast]);

  useEffect(() => {
    if (!component || !version) return;
    let live = true;
    setView(null);
    contentApi
      .version(component.id, version, roomId)
      .then((v) => live && setView(v))
      .catch((e) => live && toast.error(apiErrorText(e, "Could not load that version")));
    return () => {
      live = false;
    };
  }, [component, version, roomId, toast]);

  const download = async (url: string, v: string) => {
    if (!component) return;
    try {
      saveBlob(await contentApi.bundle(url, roomId), `${component.name}-${v}.tar`);
    } catch (e) {
      toast.error(apiErrorText(e, "Could not download the bundle"));
    }
  };

  const c = view?.component ?? component;
  return (
    <Dialog
      open={!!component}
      onClose={onClose}
      size="lg"
      title={c ? contentLabel(c) : undefined}
      description={c?.description || undefined}
      footer={
        c?.mine && onImprove ? (
          <Button variant="primary" onClick={() => onImprove(c)}>
            <SparkleIcon size={16} weight="fill" />
            Improve with AI
          </Button>
        ) : undefined
      }
    >
      {c && (
        <div className="flex flex-col gap-4 text-sm">
          <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-fg-subtle)]">
            <Badge>{c.kind === "expert" ? "Expert" : "Skill pack"}</Badge>
            <span className="font-mono">{c.name}</span>
            <span>
              <ContentByline component={c} />
            </span>
            {detail && detail.versions.length > 1 ? (
              <Select value={version} onChange={(e) => setVersion(e.target.value)} className="h-8 w-36 text-xs" aria-label="Version">
                {detail.versions.map((v) => (
                  <option key={v.version} value={v.version}>
                    v{v.version}
                    {v.version === c.latestVersion ? " (latest)" : ""}
                  </option>
                ))}
              </Select>
            ) : (
              <span>v{version}</span>
            )}
          </div>
          {(isTeam(c) || usageLine(c)) && (
            <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-fg-subtle)]">
              {isTeam(c) && <TeamBadge />}
              {usageLine(c) && <span>{usageLine(c)}</span>}
            </div>
          )}
          <PermissionBadges component={c} />
          {c.needsSandbox && !c.mine && (
            <p className="text-xs text-[var(--color-fg-muted)]">It runs scripts or tools, so turns that use it run in the agent sandbox.</p>
          )}
          {!view ? (
            <p className="text-[var(--color-fg-muted)]">Loading…</p>
          ) : (
            <>
              {view.instructions?.trim() && (
                <section>
                  <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Instructions</h3>
                  <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded-[var(--radius)] bg-[var(--color-surface-2)] p-3 text-xs">{view.instructions.trim()}</pre>
                </section>
              )}
              {isTeam(c) && c.team && (
                <section>
                  <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Team</h3>
                  <TeamRoster team={c.team} />
                </section>
              )}
              {c.welcome && (
                <section>
                  <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Welcome</h3>
                  <p className="whitespace-pre-wrap text-[var(--color-fg-muted)]">{c.welcome}</p>
                </section>
              )}
              {(c.quickPrompts?.length ?? 0) > 0 && (
                <section>
                  <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Quick prompts</h3>
                  <ul className="flex flex-col gap-1">
                    {c.quickPrompts?.map((q) => (
                      <li key={q.title}>
                        <span className="font-medium">{q.title}</span> <span className="text-[var(--color-fg-muted)]">— {q.prompt}</span>
                      </li>
                    ))}
                  </ul>
                </section>
              )}
              {(c.skills?.length ?? 0) > 0 && (
                <section>
                  <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Skills</h3>
                  <div className="flex flex-col gap-2">
                    {c.skills?.map((s) => (
                      <details key={s.name} className="rounded-[var(--radius)] border border-[var(--color-border)] p-2">
                        <summary className="cursor-pointer">
                          <span className="font-mono">{s.name}</span> <span className="text-[var(--color-fg-muted)]">— {s.description}</span>
                        </summary>
                        <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap text-xs">{view.skillBodies?.[s.name]?.trim() || "(empty)"}</pre>
                      </details>
                    ))}
                  </div>
                </section>
              )}
              {(c.requires.skills?.length ?? 0) > 0 && (
                <p className="text-xs text-[var(--color-fg-muted)]">Needs skill packs: {c.requires.skills?.join(", ")}</p>
              )}
              <section>
                <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Files</h3>
                <ul className="flex flex-col gap-0.5 font-mono text-xs">
                  {view.files.map((f) => (
                    <li key={f.path} className="flex gap-2">
                      <span className="min-w-0 flex-1 truncate">{f.path}</span>
                      <span className="text-[var(--color-fg-subtle)]">{formatSize(f.sizeBytes)}</span>
                    </li>
                  ))}
                </ul>
              </section>
            </>
          )}
          {detail && (
            <section>
              <h3 className="mb-1 text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">Versions</h3>
              <ul className="flex flex-col gap-1">
                {detail.versions.map((v) => (
                  <li key={v.version} className="flex items-center gap-2 text-xs">
                    <span className="w-16 font-mono">v{v.version}</span>
                    <span className="min-w-0 flex-1 truncate text-[var(--color-fg-subtle)]">
                      {v.createdAtMs > 0 ? `${relativeTime(v.createdAtMs)} · ` : ""}
                      {formatSize(v.sizeBytes)}
                    </span>
                    <button
                      type="button"
                      onClick={() => void download(v.bundleUrl, v.version)}
                      title="Download bundle"
                      aria-label={`Download version ${v.version}`}
                      className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
                    >
                      <DownloadSimpleIcon size={15} />
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </div>
      )}
    </Dialog>
  );
}
