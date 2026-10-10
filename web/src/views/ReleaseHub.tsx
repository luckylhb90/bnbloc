import { useCallback, useEffect, useState } from "react";
import { AppleLogoIcon, PackageIcon } from "@phosphor-icons/react";
import { api, type ReleaseEntry, type ReleaseFile, type ReleaseHub as ReleaseHubPayload } from "@/lib/api";
import { formatBytes } from "@/lib/format";
import { hasMacInstaller, openMacInstallGuide } from "@/lib/macInstall";
import { Markdown } from "@/lib/markdown";
import { Button } from "@/ui/Button";
import { Badge, Card } from "@/ui/Card";
import { ReleaseDownloadButton } from "@/views/ReleaseDownloadButton";

const FALLBACK_PROJECT_NAME = "bnbloc";

export function ReleaseHub() {
  const [hub, setHub] = useState<ReleaseHubPayload | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      setHub(await api.getAppReleases());
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "failed to load releases");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading) {
    return (
      <div className="mx-auto flex w-full max-w-4xl flex-col gap-4 p-4">
        <HeaderSkeleton />
        <Card className="p-4 text-sm text-[var(--color-fg-muted)]">Loading releases…</Card>
      </div>
    );
  }

  const project = hub?.project;
  const projectName = project?.name.trim() || FALLBACK_PROJECT_NAME;
  const releases = hub?.releases ?? [];

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-6 p-4">
      <header className="flex items-start gap-3">
        <div className="grid size-10 shrink-0 place-items-center rounded-[var(--radius-lg)] border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] text-[var(--color-accent)]">
          <PackageIcon size={22} weight="fill" />
        </div>
        <div className="min-w-0 flex-1">
          <p className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
            Release Hub
          </p>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight text-[var(--color-fg)]">
            {projectName}
          </h1>
          {project?.tagline.trim() && (
            <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{project.tagline}</p>
          )}
        </div>
      </header>

      {project?.intro.trim() && (
        <Card className="p-4 text-sm leading-relaxed text-[var(--color-fg-muted)]">
          <Markdown text={project.intro} />
        </Card>
      )}

      {error && (
        <Card className="border-[var(--color-danger)]/30 bg-[var(--color-danger)]/10 p-4">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-[var(--color-danger)]">{error}</p>
            <Button size="sm" variant="outline" onClick={() => void load()}>
              Retry
            </Button>
          </div>
        </Card>
      )}

      {!error && releases.length === 0 && (
        <Card className="border-dashed p-8 text-center">
          <p className="text-sm font-medium text-[var(--color-fg)]">还没有发布版本 / No releases yet</p>
          <p className="mt-1 text-sm text-[var(--color-fg-muted)]">
            Published builds and download links will appear here when they are available.
          </p>
        </Card>
      )}

      {releases.length > 0 && (
        <section className="flex flex-col gap-3">
          <div className="flex flex-wrap items-end justify-between gap-2">
            <SectionHeading title="Version history" subtitle="Newest releases first, maintained by this bnbloc instance." />
            {hasMacInstaller(releases) && (
              <Button size="sm" variant="ghost" onClick={openMacInstallGuide}>
                <AppleLogoIcon size={14} weight="fill" /> Trouble opening the app?
              </Button>
            )}
          </div>
          {releases.map((release) => (
            <ReleaseCard key={`${release.version}:${release.channel}`} release={release} />
          ))}
        </section>
      )}
    </div>
  );
}

function ReleaseCard({ release }: { readonly release: ReleaseEntry }) {
  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="tabular rounded-full border border-[var(--color-border-strong)] bg-[var(--color-elevated)] px-2.5 py-1 text-sm font-semibold text-[var(--color-fg)]">
              v{release.version || "unknown"}
            </span>
            <Badge tone={release.channel === "stable" ? "accent" : "warning"}>{release.channel || "channel"}</Badge>
          </div>
          <p className="mt-2 text-sm text-[var(--color-fg-muted)]">
            {formatReleaseDate(release.releaseDate)}
          </p>
        </div>
      </div>

      <div className="mt-4 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3 text-sm leading-relaxed text-[var(--color-fg-muted)]">
        {release.notes.trim() ? <Markdown text={release.notes} /> : "No changelog notes for this release."}
      </div>

      <div className="mt-4 flex flex-col gap-2">
        {release.files.length > 0 ? (
          release.files.map((file) => <DownloadRow key={`${file.platform}:${file.arch}:${file.name}`} file={file} />)
        ) : (
          <div className="rounded-[var(--radius)] border border-dashed border-[var(--color-border)] px-4 py-3 text-sm text-[var(--color-fg-subtle)]">
            No downloadable files listed for this version.
          </div>
        )}
      </div>
    </Card>
  );
}

function DownloadRow({ file }: { readonly file: ReleaseFile }) {
  const safe = isSafeHttpUrl(file.url);
  const sha = file.sha256?.trim();
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-3">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium text-[var(--color-fg)]">{file.name || "download"}</span>
          <Badge tone="neutral">{file.platform || "platform"}</Badge>
          <Badge tone="neutral">{file.arch || "arch"}</Badge>
          {file.target?.trim() && <Badge tone="neutral">{file.target}</Badge>}
        </div>
        <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-[var(--color-fg-subtle)]">
          {typeof file.size === "number" && file.size >= 0 && <span>{formatBytes(file.size)}</span>}
          {sha && <span className="tabular" title={sha}>sha256 {truncateSha256(sha)}</span>}
        </div>
      </div>
      {safe ? (
        <ReleaseDownloadButton file={file} className="ml-auto" />
      ) : (
        <Button size="sm" variant="secondary" disabled className="ml-auto">
          Unavailable
        </Button>
      )}
    </div>
  );
}

function SectionHeading({ title, subtitle }: { readonly title: string; readonly subtitle: string }) {
  return (
    <div>
      <h2 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg)]">{title}</h2>
      <p className="mt-1 text-sm text-[var(--color-fg-muted)]">{subtitle}</p>
    </div>
  );
}

function HeaderSkeleton() {
  return (
    <header className="flex items-start gap-3">
      <div className="grid size-10 place-items-center rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)] text-[var(--color-fg-subtle)]">
        <PackageIcon size={22} />
      </div>
      <div>
        <p className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">Release Hub</p>
        <h1 className="mt-1 text-2xl font-semibold tracking-tight text-[var(--color-fg)]">{FALLBACK_PROJECT_NAME}</h1>
      </div>
    </header>
  );
}

function formatReleaseDate(value: string): string {
  const text = value.trim();
  if (!text) return "Release date not recorded";
  const date = new Date(text);
  if (Number.isNaN(date.getTime())) return text;
  return date.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

function truncateSha256(value: string): string {
  return value.length > 16 ? `${value.slice(0, 12)}…${value.slice(-4)}` : value;
}

function isSafeHttpUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:";
  } catch (e: unknown) {
    if (e instanceof TypeError) return false;
    throw e;
  }
}
