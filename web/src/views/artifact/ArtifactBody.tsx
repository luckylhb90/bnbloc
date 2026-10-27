import { Component, lazy, Suspense, useEffect, useMemo, useState, type ReactNode } from "react";
import { DownloadSimpleIcon, FileIcon } from "@phosphor-icons/react";
import { formatBytes } from "@/lib/format";
import { isChunkLoadError } from "@/lib/webBuild";
import { reportStaleBuild } from "@/views/WebUpdateNotice";
import { storeRoomCover } from "@/views/workbench/artifactData";
import { TEXT_VIEWER_ID } from "./builtin";
import {
  listArtifactViewers,
  selectViewer,
  type ArtifactFileInfo,
  type ArtifactViewer,
  type ArtifactViewerProps,
  type ViewerSelection,
  type SiteSource,
  type ViewerToolbarItem,
  type ViewerToolbarValue,
} from "./registry";
import { createToolbarHost } from "./toolbar";

// ArtifactBody hosts ONE viewer for one version's data — the bytes, or for a
// web app (`site`) the source of its frame URL — whoever fetched them (the
// workbench with the member's session, the public share page from the
// public routes; the viewers never fetch for themselves). It selects the
// viewer from the registry (re-selecting when a previewer component is
// installed or removed at runtime), decodes text for text viewers, wires the
// viewer's toolbar to the tab, and contains a viewer crash. With no viewer
// for the type it renders a download card. A cover the viewer draws goes
// where the host says (onCover: a work's detail stores it as the work's),
// by default to the room version's thumbnail (storeRoomCover); without a
// room or an artifact nowhere.

const EMPTY_BLOB = new Blob([]);

const lazyViewers = new WeakMap<ArtifactViewer, React.LazyExoticComponent<React.ComponentType<ArtifactViewerProps>>>();

function lazyViewer(viewer: ArtifactViewer) {
  let component = lazyViewers.get(viewer);
  if (!component) {
    component = lazy(viewer.load);
    lazyViewers.set(viewer, component);
  }
  return component;
}

/** The viewer a file opens with (the member's choice when still valid); the
 *  plain-text viewer backs inline text no viewer claims. */
export function resolveViewer(file: ArtifactFileInfo, preferredId: string | undefined, inlineText: boolean): ArtifactViewer | null {
  const chosen = selectViewer(file, preferredId);
  if (chosen || !inlineText) return chosen;
  return listArtifactViewers().find((v) => v.id === TEXT_VIEWER_ID) ?? null;
}

class ViewerBoundary extends Component<{ children: ReactNode; resetKey: string }, { error: Error | null; key: string }> {
  state = { error: null as Error | null, key: this.props.resetKey };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  static getDerivedStateFromProps(props: { resetKey: string }, state: { error: Error | null; key: string }) {
    return props.resetKey === state.key ? null : { error: null, key: props.resetKey };
  }

  componentDidCatch(error: Error) {
    console.error("bnbloc viewer error:", error);
    if (isChunkLoadError(error)) reportStaleBuild();
  }

  render() {
    if (!this.state.error) return this.props.children;
    // The viewer's code is a file a deployment replaced: not a broken viewer,
    // a page that is older than the server. Say that and offer the reload.
    if (isChunkLoadError(this.state.error)) {
      return (
        <div className="flex flex-col items-start gap-3 p-4 text-sm text-[var(--color-fg-muted)]">
          <p>bnbloc was updated while this page was open, so this viewer is no longer available here. Reload to open the file.</p>
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="tap tactile rounded-[var(--radius)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 py-1.5 text-[var(--color-fg)] hover:bg-[var(--color-surface-2)]"
          >
            Reload
          </button>
        </div>
      );
    }
    return (
      <p className="p-4 text-sm text-[var(--color-danger,#f87171)]">
        This viewer failed: {this.state.error.message || "unknown error"}
      </p>
    );
  }
}

export function FileFallback({
  file,
  reason,
  onDownload,
}: {
  readonly file: ArtifactFileInfo;
  readonly reason?: string;
  readonly onDownload?: () => void;
}) {
  return (
    <div className="grid h-full place-items-center p-6">
      <div className="flex w-full max-w-sm flex-col items-center gap-3 rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-6 text-center">
        <FileIcon size={36} className="text-[var(--color-fg-subtle)]" />
        <div className="min-w-0 max-w-full">
          <p className="truncate text-sm font-medium text-[var(--color-fg)]">{file.name}</p>
          <p className="text-xs text-[var(--color-fg-subtle)]">
            {[file.mimeType || "Unknown type", file.sizeBytes !== undefined ? formatBytes(file.sizeBytes) : ""]
              .filter(Boolean)
              .join(" · ")}
          </p>
        </div>
        <p className="text-xs text-[var(--color-fg-muted)]">{reason ?? "No preview is available for this file type."}</p>
        {onDownload && (
          <button
            type="button"
            onClick={onDownload}
            className="tactile inline-flex items-center gap-1.5 rounded-[var(--radius)] bg-[var(--color-accent)] px-3 py-1.5 text-xs font-medium text-[var(--color-accent-fg,#04120c)]"
          >
            <DownloadSimpleIcon size={14} /> Download
          </button>
        )}
      </div>
    </div>
  );
}

export function ArtifactBody({
  roomId,
  actor,
  artifactId,
  file,
  blob,
  site = null,
  viewer,
  onDownload,
  onToolbar,
  onLocalState,
  remoteState,
  onSelection,
  reveal,
  onPublish,
  onCover,
  onOpenFile,
  viewOnly,
  onRendered,
}: {
  readonly roomId: string;
  readonly actor?: string;
  readonly artifactId: string;
  readonly file: ArtifactFileInfo;
  // The version's bytes; null for a site viewer (it needs none).
  readonly blob: Blob | null;
  // A site version's frame URL source (site viewers only).
  readonly site?: SiteSource | null;
  readonly viewer: ArtifactViewer | null;
  readonly onDownload?: () => void;
  // The viewer's toolbar items and the gesture entry point, for the tab
  // toolbar to render; called with [] when the viewer unmounts.
  readonly onToolbar?: (items: readonly ViewerToolbarItem[], emit: (id: string, value: ViewerToolbarValue) => void) => void;
  readonly onLocalState?: (state: unknown) => void;
  readonly remoteState?: { state: unknown; actor: string; nonce: number };
  readonly onSelection?: (selection: ViewerSelection | null) => void;
  readonly reveal?: ArtifactViewerProps["reveal"];
  readonly onPublish?: ArtifactViewerProps["onPublish"];
  // Where a cover the viewer draws goes (default: the room version's
  // thumbnail).
  readonly onCover?: ArtifactViewerProps["onCover"];
  readonly onOpenFile?: ArtifactViewerProps["onOpenFile"];
  // The viewer is for looking only (a share page), and how it says it has drawn.
  readonly viewOnly?: ArtifactViewerProps["viewOnly"];
  readonly onRendered?: ArtifactViewerProps["onRendered"];
}) {
  const instanceKey = `${viewer?.id ?? ""}:${artifactId}:${file.version}`;
  const onToolbarRef = useStableRef(onToolbar);
  // A fresh sink per viewer instance: gestures never reach a replaced viewer.
  const toolbarHost = useMemo(() => {
    const host = createToolbarHost((items) => onToolbarRef.current?.(items, host.emit));
    return host;
  }, [instanceKey, onToolbarRef]);

  const roomCover = useMemo<ArtifactViewerProps["onCover"]>(
    () =>
      roomId && artifactId
        ? (png, source) => void storeRoomCover(roomId, artifactId, file.version, png, source).catch(() => {})
        : undefined,
    [roomId, artifactId, file.version],
  );

  const wantsText = viewer?.input === "text";
  const [text, setText] = useState<{ blob: Blob; text: string } | null>(null);
  useEffect(() => {
    if (!wantsText || !blob) return;
    let cancelled = false;
    void blob.text().then((t) => {
      if (!cancelled) setText({ blob, text: t });
    });
    return () => {
      cancelled = true;
    };
  }, [blob, wantsText]);

  const siteViewer = viewer?.input === "site";
  // A viewer without its data source (a site without a site URL, bytes not
  // handed over) degrades to the download card instead of rendering blank.
  if (!viewer || (siteViewer ? !site : !blob)) return <FileFallback file={file} onDownload={onDownload} />;
  if (wantsText && text?.blob !== blob) {
    return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Loading preview…</p>;
  }
  const Viewer = lazyViewer(viewer);
  const props: ArtifactViewerProps = {
    roomId,
    actor,
    artifactId,
    version: file.version,
    file,
    blob: blob ?? EMPTY_BLOB,
    site: siteViewer ? site : null,
    content: wantsText ? text?.text ?? "" : "",
    toolbar: toolbarHost,
    onLocalState,
    remoteState,
    onSelection,
    reveal,
    onPublish,
    onCover: onCover ?? roomCover,
    onOpenFile,
    viewOnly,
    onRendered,
  };
  return (
    <ViewerBoundary resetKey={instanceKey}>
      <Suspense fallback={<p className="p-4 text-sm text-[var(--color-fg-subtle)]">Loading preview…</p>}>
        <Viewer key={instanceKey} {...props} />
      </Suspense>
    </ViewerBoundary>
  );
}

function useStableRef<T>(value: T): { readonly current: T } {
  const [ref] = useState(() => ({ current: value }));
  ref.current = value;
  return ref;
}
