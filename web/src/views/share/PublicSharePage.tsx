import { useCallback, useEffect, useState } from "react";
import { DownloadSimpleIcon, LinkBreakIcon, LockSimpleIcon } from "@phosphor-icons/react";
import { ApiError } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { fileCategory, fileTypeLabel, isTextual, type ArtifactFileFacts } from "@/lib/artifactFiles";
import { formatBytes } from "@/lib/format";
import { beginShareSignIn } from "@/lib/loginReturn";
import { expiresLabel, openShareLink, shareVersionLabel, type PublicShare, type ShareOpening } from "@/lib/shares";
import { publicShareApi, sharesApi } from "@/lib/sharesApi";
import "@/views/artifact/builtin";
import { ArtifactBody, resolveViewer } from "@/views/artifact/ArtifactBody";
import { useViewerRegistry } from "@/views/artifact/registry";
import { fileInfoFromFacts } from "@/views/artifact/fileInfo";
import type { ViewerToolbarItem, ViewerToolbarValue } from "@/views/artifact/registry";
import { ViewerToolbar } from "@/views/artifact/toolbar";
import { categoryIcons } from "@/views/workbench/fileIcons";
import { SharedApp } from "./SharedApp";

// PublicSharePage is what a link's holder sees at the share page's hash
// (lib/hashRoute shareHash; a share link `<origin>/s/<token>` redirects there)
// (docs/prd/2026-10-13-share-link-sign-in.md): nothing of the room but the
// shared version. A link anyone can open renders at once, without a session.
// A link that needs sign-in (for the room's organization, the default, or
// for anyone signed in) answers the page's anonymous request 401: without a
// session the page remembers itself (lib/loginReturn) and sends the person
// to the app's sign-in, which brings them back here; with one it mints a
// share ticket (the only request that carries the session) and opens the
// link with the ticket in the token's place (lib/shares openShareLink).
// Someone outside the organization of an organization's link is told whose
// link it is; a link for anyone signed in never says so.
//
// A web app (a version the gateway serves as a site: `siteUrl`, carrying
// the same credential) is the app itself, full-screen (SharedApp). Any
// other file gets the file page: the product name, the file's title, who
// shared it, when the link expires, a Download button, and the file
// rendered by the same viewer registry as the workbench (bytes from the
// link's content route). A dead link (unknown, expired, revoked, switched
// off) is one "no longer available" page: the gateway answers 404 for all
// of them alike.

type Load = { kind: "loading" } | { kind: "gone" } | { kind: "error" } | ShareOpening;

const PRODUCT = "bnbloc";

const openApi = { get: publicShareApi.get, ticket: sharesApi.ticket };

export function PublicSharePage({ token }: { readonly token: string }) {
  const { ready, subject } = useAuth();
  const signedIn = subject !== null;
  const [load, setLoad] = useState<Load>({ kind: "loading" });
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!ready) return;
    let cancelled = false;
    setLoad({ kind: "loading" });
    openShareLink(token, signedIn, openApi)
      .then((next) => {
        if (!cancelled) setLoad(next);
      })
      .catch((e: unknown) => {
        if (!cancelled) setLoad({ kind: e instanceof ApiError && e.status === 404 ? "gone" : "error" });
      });
    return () => {
      cancelled = true;
    };
  }, [token, attempt, ready, signedIn]);

  // A link that needs sign-in, without a session: sign in, then come back.
  useEffect(() => {
    if (load.kind !== "signin") return;
    const url = beginShareSignIn(token, window.location.href, Date.now());
    if (url) window.location.replace(url);
  }, [load.kind, token]);

  if (load.kind === "ready") {
    const { share, link, visitor } = load;
    return share.siteUrl ? (
      <SharedApp token={token} visitor={visitor} share={{ ...share, siteUrl: share.siteUrl }} />
    ) : (
      <SharedFile link={link} share={share} />
    );
  }
  if (load.kind === "orgOnly") {
    return (
      <Frame>
        <div className="grid flex-1 place-items-center p-6 text-center">
          <div className="flex max-w-sm flex-col items-center gap-3">
            <LockSimpleIcon size={36} className="text-[var(--color-fg-subtle)]" aria-hidden />
            <h1 className="text-lg font-semibold tracking-tight">{load.message}</h1>
            <p className="text-sm text-[var(--color-fg-muted)]">
              You are signed in with an account outside that organization. Ask the person who shared it for access.
            </p>
          </div>
        </div>
      </Frame>
    );
  }
  return (
    <Frame>
      <div className="grid flex-1 place-items-center p-6 text-center">
        {load.kind === "loading" || load.kind === "signin" ? (
          <div
            role="status"
            aria-label={load.kind === "signin" ? "Taking you to sign in" : "Loading"}
            className="size-6 animate-spin rounded-full border-2 border-[var(--color-border-strong)] border-t-[var(--color-accent)]"
          />
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
function usePublicBytes(link: string, wanted: boolean, mimeType: string): { blob: Blob | null; error: boolean } {
  const [state, setState] = useState<{ blob: Blob | null; error: boolean }>({ blob: null, error: false });
  useEffect(() => {
    if (!wanted) return;
    let cancelled = false;
    publicShareApi
      .content(link)
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
  }, [link, wanted, mimeType]);
  return state;
}

function SharedFile({ link, share }: { readonly link: string; readonly share: PublicShare }) {
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
  const { blob, error } = usePublicBytes(link, viewer !== null && !runsSite, file.mimeType);
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

  const download = publicShareApi.downloadUrl(link);
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
