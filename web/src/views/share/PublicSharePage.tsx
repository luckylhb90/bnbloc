import { useCallback, useEffect, useState } from "react";
import { DownloadSimpleIcon, LinkBreakIcon } from "@phosphor-icons/react";
import { ApiError } from "@/lib/api";
import { fileCategory, fileTypeLabel, isTextual, type ArtifactFileFacts } from "@/lib/artifactFiles";
import { formatBytes } from "@/lib/format";
import { expiresLabel, shareVersionLabel, type PublicShare } from "@/lib/shares";
import { publicShareApi } from "@/lib/sharesApi";
import "@/views/artifact/builtin";
import { ArtifactBody, resolveViewer, useViewerRegistry } from "@/views/artifact/ArtifactBody";
import { fileInfoFromFacts } from "@/views/artifact/fileInfo";
import type { ViewerToolbarItem, ViewerToolbarValue } from "@/views/artifact/registry";
import { ViewerToolbar } from "@/views/artifact/toolbar";
import { categoryIcons } from "@/views/workbench/fileIcons";
import { SharedApp } from "./SharedApp";

// PublicSharePage is what someone outside the room sees at `#/share/<token>`:
// no sign-in, no session, nothing of the room. A web app (a version the
// gateway serves as a site: `siteUrl`) is the app itself, full-screen
// (SharedApp). Any other file gets the file page: the product name, the
// file's title, who shared it, when the link expires, a Download button, and
// the file rendered by the same viewer registry as the workbench (bytes from
// the public content route). It calls only the public share routes
// (lib/sharesApi publicShareApi). A dead link (unknown, expired, revoked,
// switched off) is one "no longer available" page: the gateway answers 404
// for all of them alike.

type Load = { kind: "loading" } | { kind: "gone" } | { kind: "error" } | { kind: "ready"; share: PublicShare };

const PRODUCT = "bnbloc";

export function PublicSharePage({ token }: { readonly token: string }) {
  const [load, setLoad] = useState<Load>({ kind: "loading" });
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let cancelled = false;
    setLoad({ kind: "loading" });
    publicShareApi
      .get(token)
      .then((share) => {
        if (!cancelled) setLoad({ kind: "ready", share });
      })
      .catch((e: unknown) => {
        if (!cancelled) setLoad({ kind: e instanceof ApiError && e.status === 404 ? "gone" : "error" });
      });
    return () => {
      cancelled = true;
    };
  }, [token, attempt]);

  if (load.kind === "ready") {
    const { share } = load;
    return share.siteUrl ? <SharedApp token={token} share={{ ...share, siteUrl: share.siteUrl }} /> : <SharedFile token={token} share={share} />;
  }
  return (
    <Frame>
      <div className="grid flex-1 place-items-center p-6 text-center">
        {load.kind === "loading" ? (
          <div role="status" aria-label="Loading" className="size-6 animate-spin rounded-full border-2 border-[var(--color-border-strong)] border-t-[var(--color-accent)]" />
        ) : (
          <div className="flex max-w-sm flex-col items-center gap-3">
            <LinkBreakIcon size={36} className="text-[var(--color-fg-subtle)]" aria-hidden />
            <h1 className="text-lg font-semibold tracking-tight">
              {load.kind === "gone" ? "This link is no longer available" : "Could not load this link"}
            </h1>
            <p className="text-sm text-[var(--color-fg-muted)]">
              {load.kind === "gone"
                ? "It may have expired or been revoked. Ask the person who shared it for a new link."
                : "Check your connection and try again."}
            </p>
            {load.kind === "error" && (
              <button
                type="button"
                onClick={() => setAttempt((n) => n + 1)}
                className="tactile rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-1.5 text-sm hover:bg-[var(--color-surface-2)]"
              >
                Try again
              </button>
            )}
          </div>
        )}
      </div>
    </Frame>
  );
}

function Frame({ children, download }: { readonly children: React.ReactNode; readonly download?: string }) {
  return (
    <div className="flex h-[var(--app-height,100dvh)] flex-col bg-[var(--color-bg)] text-[var(--color-fg)]">
      <header className="flex shrink-0 items-center gap-3 border-b border-[var(--color-border)] px-4 pb-2.5 pt-[calc(0.625rem+env(safe-area-inset-top))]">
        <span className="text-base font-semibold tracking-tight">{PRODUCT}</span>
        {download && (
          <a
            href={download}
            rel="noreferrer"
            className="tactile ml-auto inline-flex h-9 items-center gap-1.5 rounded-[var(--radius)] bg-[var(--color-accent)] px-3 text-sm font-medium text-[var(--color-accent-fg,#04120c)] hover:bg-[var(--color-accent-hover)]"
          >
            <DownloadSimpleIcon size={16} /> Download
          </a>
        )}
      </header>
      {children}
    </div>
  );
}

/** The link's bytes for a byte-reading viewer (none for a site or a type no
 *  viewer renders). */
function usePublicBytes(token: string, wanted: boolean, mimeType: string): { blob: Blob | null; error: boolean } {
  const [state, setState] = useState<{ blob: Blob | null; error: boolean }>({ blob: null, error: false });
  useEffect(() => {
    if (!wanted) return;
    let cancelled = false;
    publicShareApi
      .content(token)
      .then((b) => {
        const typed = mimeType && b.type !== mimeType ? b.slice(0, b.size, mimeType) : b;
        if (!cancelled) setState({ blob: typed, error: false });
      })
      .catch(() => {
        if (!cancelled) setState({ blob: null, error: true });
      });
    return () => {
      cancelled = true;
    };
  }, [token, wanted, mimeType]);
  return state;
}

function SharedFile({ token, share }: { readonly token: string; readonly share: PublicShare }) {
  useViewerRegistry();
  const facts: ArtifactFileFacts = {
    kind: share.artifact.kind,
    title: share.artifact.title,
    fileName: share.artifact.fileName,
    mimeType: share.version.mimeType,
  };
  const file = fileInfoFromFacts(facts, share.version.version, share.version.sizeBytes);
  const viewer = resolveViewer(file, undefined, isTextual(facts));
  // A site the gateway does not serve has no frame and no bytes here: the
  // file fallback offers the download.
  const runsSite = viewer?.input === "site";
  const { blob, error } = usePublicBytes(token, viewer !== null && !runsSite, file.mimeType);
  const [tools, setTools] = useState<{ items: readonly ViewerToolbarItem[]; emit: (id: string, value: ViewerToolbarValue) => void }>({
    items: [],
    emit: () => {},
  });
  const onToolbar = useCallback(
    (items: readonly ViewerToolbarItem[], emit: (id: string, value: ViewerToolbarValue) => void) => setTools({ items, emit }),
    [],
  );
  useEffect(() => {
    document.title = `${file.name} · ${PRODUCT}`;
  }, [file.name]);

  const download = publicShareApi.downloadUrl(token);
  const Icon = categoryIcons[fileCategory(facts)];
  const details = [
    `Shared by ${share.sharedBy.displayName}`,
    expiresLabel(share.expiresAtMs, Date.now()),
    share.followsLatest ? shareVersionLabel(0) : shareVersionLabel(share.version.version),
    share.version.sizeBytes > 0 ? formatBytes(share.version.sizeBytes) : "",
  ].filter(Boolean);

  return (
    <Frame download={download}>
      <div className="shrink-0 border-b border-[var(--color-border)] px-4 py-3">
        <div className="flex min-w-0 items-center gap-2">
          <Icon size={18} className="shrink-0 text-[var(--color-accent)]" aria-hidden />
          <h1 className="min-w-0 truncate text-base font-semibold tracking-tight" title={share.artifact.title}>
            {file.name}
          </h1>
          <span className="shrink-0 rounded-full border border-[var(--color-border)] px-1.5 text-[10px] uppercase tracking-wide text-[var(--color-fg-subtle)]">
            {fileTypeLabel(facts)}
          </span>
        </div>
        <p className="mt-1 text-xs text-[var(--color-fg-muted)]">{details.join(" · ")}</p>
        {share.version.summary && <p className="mt-1 line-clamp-2 text-sm text-[var(--color-fg-muted)]">{share.version.summary}</p>}
      </div>
      {tools.items.length > 0 && (
        <div className="shrink-0 border-b border-[var(--color-border)] px-2 py-1">
          <ViewerToolbar items={tools.items} onAction={tools.emit} />
        </div>
      )}
      <main className="relative min-h-0 flex-1 overflow-hidden" aria-label={file.name}>
        {error ? (
          <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Could not load this file. Use Download to get a copy.</p>
        ) : viewer && !runsSite && !blob ? (
          <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Loading preview…</p>
        ) : (
          <ArtifactBody
            roomId=""
            artifactId="shared"
            file={file}
            blob={blob}
            viewer={viewer}
            onDownload={() => window.location.assign(download)}
            onToolbar={onToolbar}
          />
        )}
      </main>
    </Frame>
  );
}
