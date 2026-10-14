import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Select } from "@/ui/Select";
import {
  ArrowCounterClockwiseIcon,
  ArrowSquareOutIcon,
  CheckIcon,
  CornersInIcon,
  CornersOutIcon,
  DownloadSimpleIcon,
  GitDiffIcon,
  LinkIcon,
  PencilSimpleIcon,
  ShareNetworkIcon,
  SparkleIcon,
} from "@phosphor-icons/react";
import { api } from "@/lib/api";
import { downloadFileName, fileCategory, fileTypeLabel, isSite, isTextual, versionFacts } from "@/lib/artifactFiles";
import { cn } from "@/lib/cn";
import { appLinkBase } from "@/lib/config";
import { formatBytes, relativeTime } from "@/lib/format";
import { artifactLinkUrl } from "@/lib/hashRoute";
import { saveBlob } from "@/lib/saveBlob";
import { sharesApi } from "@/lib/sharesApi";
import { useToast } from "@/ui/Toast";
import { useCopy } from "@/ui/useCopy";
import "@/views/artifact/builtin";
import { ArtifactBody, resolveViewer } from "@/views/artifact/ArtifactBody";
import { useViewerRegistry } from "@/views/artifact/registry";
import { OpenWithMenu } from "@/views/artifact/OpenWithMenu";
import { preferredViewerFor } from "@/lib/viewerPreference";
import type { SiteSource, ViewerSelection, ViewerToolbarItem, ViewerToolbarValue } from "@/views/artifact/registry";
import { ViewerToolbar } from "@/views/artifact/toolbar";
import {
  fileInfoFor,
  invalidateArtifacts,
  loadVersionBlob,
  useArtifactDetail,
  useVersionBlob,
  versionMeta,
  type ArtifactDetail,
} from "./artifactData";
import { AppDataLine } from "./AppDataLine";
import { categoryIcons } from "./fileIcons";
import { ShareDialog } from "./ShareDialog";
import { CompareView } from "./CompareView";
import { selectComparer, useComparerRegistry } from "@/views/artifact/compare/registry";
import { FileAskPanel, useFileAsk, type FileComposerOptions } from "./FileAskPanel";
import { requestSelection } from "./fileAsk";
import { fileAskRequests } from "@/lib/team";
import { LocalSaveChip } from "./LocalSaveChip";
import { useDisplayNames } from "@/lib/useDisplayName";
import { errorText } from "@/lib/errors";

// ArtifactTabView is one workbench tab: a deliverable with its toolbar —
// title and type, the version picker (any version; compare any two text
// versions; restore an older one as a new version), download, copy link,
// share (public link), open in a new window, fullscreen, and the viewer's
// own controls — over the viewer itself; a web app adds its owners' data
// line (AppDataLine) between them. The desktop pop-out window hosts
// exactly this view. A tab that follows the latest version shows a brief
// "Updated" mark when a new version arrives (the viewer reloads with it).

type Mode = "view" | "compare" | "edit";

export const ACTION =
  "tap tactile grid size-8 shrink-0 place-items-center rounded-[var(--radius)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] disabled:opacity-40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60";
export const CHIP =
  "tactile inline-flex h-8 items-center gap-1 rounded-[var(--radius)] px-2 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] disabled:opacity-40";
const SELECT =
  "h-8 min-w-0 max-w-[13rem] rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-1.5 text-xs text-[var(--color-fg)]";

function versionLabel(detail: ArtifactDetail, n: number, nameOf: (id: string) => string): string {
  const v = versionMeta(detail, n);
  const parts = [`v${n}`];
  if (v?.authorUserId) parts.push(nameOf(v.authorUserId));
  if (v?.createdAtMs) parts.push(relativeTime(v.createdAtMs));
  if (n === detail.artifact.currentVersion) parts.push("(current)");
  return parts.join(" · ");
}

/** The absolute member link of a deliverable (gateway root in the desktop
 *  shell, this page in a browser). */
export function artifactShareUrl(roomId: string, artifactId: string, version?: number): string {
  return artifactLinkUrl(appLinkBase(), roomId, artifactId, version);
}

/** How long the "Updated" mark stays after a followed version advances. */
const UPDATED_MARK_MS = 4000;

/** The version a following tab just advanced to (cleared after a moment);
 *  null when pinned, on first load and once the mark has faded. */
function useUpdatedMark(shown: number, following: boolean): number | null {
  const [mark, setMark] = useState<number | null>(null);
  const previous = useRef({ shown, following });
  useEffect(() => {
    const before = previous.current;
    previous.current = { shown, following };
    // Only an advance while following (not the member picking a version).
    if (!following || !before.following || before.shown <= 0 || shown <= before.shown) return;
    setMark(shown);
    const timer = window.setTimeout(() => setMark(null), UPDATED_MARK_MS);
    return () => window.clearTimeout(timer);
  }, [shown, following]);
  return following ? mark : null;
}

export function ArtifactTabView({
  roomId,
  actor,
  artifactId,
  version,
  viewerId,
  onViewer,
  onVersion,
  liveStates,
  onLocalState,
  onOpenWindow,
  fullscreen,
  onToggleFullscreen,
}: {
  readonly roomId: string;
  readonly actor?: string;
  readonly artifactId: string;
  // A pinned version; undefined follows the current version live.
  readonly version?: number;
  // The member's viewer choice ("Open with…"); undefined = best match.
  readonly viewerId?: string;
  // Sets (or, with undefined, clears) the tab's viewer choice.
  readonly onViewer?: (viewerId: string | undefined) => void;
  readonly onVersion: (version: number | undefined) => void;
  readonly liveStates?: Record<string, { state: unknown; actor: string; nonce: number }>;
  readonly onLocalState?: (artifactId: string, state: unknown) => void;
  // Desktop: pop the tab out into its own window; absent in a browser (a
  // new browser tab at the deep link) and in the pop-out itself (null).
  readonly onOpenWindow?: (() => void) | null;
  readonly fullscreen?: boolean;
  readonly onToggleFullscreen?: () => void;
}) {
  const toast = useToast();
  const copy = useCopy();
  useViewerRegistry();
  useComparerRegistry();
  const { detail, error } = useArtifactDetail(roomId, artifactId);
  const nameOf = useDisplayNames((detail?.versions ?? []).map((v) => v.authorUserId ?? ""));
  const artifact = detail?.artifact ?? null;
  const shown = artifact ? (version && version <= artifact.currentVersion ? version : artifact.currentVersion) : 0;
  const meta = versionMeta(detail, shown);
  const file = artifact ? fileInfoFor(artifact, shown, meta) : null;
  const facts = artifact ? versionFacts(artifact, meta) : null;
  const textual = facts ? isTextual(facts) : false;
  const inlineText = meta?.storage !== "blob" && textual;
  // The tab's own choice, else the member's remembered choice for this file
  // type (lib/viewerPreference), else the registry's best match.
  const viewer = file ? resolveViewer(file, viewerId ?? preferredViewerFor(file), inlineText) : null;
  // A web app runs from the gateway's site routes: its archive is fetched
  // only to download or restore it.
  const runsSite = viewer?.input === "site";
  // How two versions of this file compare (null: they cannot be).
  const comparer = file && !runsSite ? selectComparer(file) : null;
  const { blob, error: blobError } = useVersionBlob(roomId, runsSite ? null : artifact, shown, meta);
  const site = useMemo<SiteSource>(
    () => ({
      resolve: () => sharesApi.siteTicket(roomId, artifactId, shown),
    }),
    [roomId, artifactId, shown],
  );
  const [sharingOpen, setSharingOpen] = useState(false);
  // The Ask AI panel (FileAskPanel): only where the room provides it.
  const ask = useFileAsk();
  const canAsk = !!ask && !!artifact && !runsSite && !artifact.isApp;
  const [asking, setAsking] = useState(false);
  // A suggestion handed over from the room, for the panel to insert.
  const [pick, setPick] = useState<FileComposerOptions["pick"]>();
  // The part of the shown version the viewer points at (preview.selection).
  const [selection, setSelection] = useState<ViewerSelection | null>(null);
  const updated = useUpdatedMark(shown, version === undefined);
  const [mode, setMode] = useState<Mode>("view");
  const [compareWith, setCompareWith] = useState<number | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [tools, setTools] = useState<{ items: readonly ViewerToolbarItem[]; emit: (id: string, value: ViewerToolbarValue) => void }>({
    items: [],
    emit: () => {},
  });
  const onToolbar = useCallback(
    (items: readonly ViewerToolbarItem[], emit: (id: string, value: ViewerToolbarValue) => void) => setTools({ items, emit }),
    [],
  );

  useEffect(() => setSelection(null), [artifactId, shown]);

  // This tab is the file in front of the room while it is mounted (only the
  // active tab is): the room's starters follow it.
  const focusKey = canAsk && file ? `${file.name}\n${file.mimeType}` : "";
  useEffect(() => {
    if (!ask || !focusKey || !file) return;
    ask.focus({ artifactId, file });
    return () => ask.focus(null);
    // The file's identity is focusKey; a re-render's new object is the same file.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ask, artifactId, focusKey]);

  // "Ask about this file" handed over (a suggestion picked in the room, a
  // local document just opened): open the panel, insert the suggestion.
  useEffect(() => {
    if (!ask) return;
    const take = () => {
      const req = fileAskRequests.peek(ask.roomId);
      if (!req || req.artifactId !== artifactId) return;
      fileAskRequests.take(ask.roomId);
      setAsking(true);
      const choice = req.choice;
      if (choice) setPick((p) => ({ choice, nonce: (p?.nonce ?? 0) + 1 }));
    };
    take();
    return fileAskRequests.subscribe((room) => {
      if (room === ask.roomId) take();
    });
  }, [ask, artifactId]);

  // Leaving compare/edit when the tab moves to another artifact.
  useEffect(() => {
    setMode("view");
    setCompareWith(null);
  }, [artifactId]);


  if (error) {
    return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">This deliverable could not be loaded. It may have been deleted, or you no longer have access.</p>;
  }
  if (!artifact || !detail || !file || !facts) {
    return <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Loading preview…</p>;
  }

  const Icon = categoryIcons[fileCategory(facts)];
  // An edit asked about an earlier version while the file changed
  // meanwhile (docs/prd/2026-10-14-ai-document-editing.md rule 4).
  const editedFrom = meta?.editedFrom ?? 0;
  const changedMeanwhile = editedFrom > 0 && meta != null && editedFrom < meta.parentVersion;
  const latest = shown === artifact.currentVersion;
  const editable = inlineText && latest;
  const versions = [...detail.versions].map((v) => v.version).sort((a, b) => b - a);
  if (!versions.includes(artifact.currentVersion) && artifact.currentVersion > 0) versions.unshift(artifact.currentVersion);

  const download = async () => {
    try {
      saveBlob(blob ?? (await loadVersionBlob(roomId, artifact, shown, meta)), downloadFileName(file));
    } catch {
      toast.error("Download failed");
    }
  };
  const copyLink = () => {
    void copy(artifactShareUrl(roomId, artifactId, version), "link");
  };
  const openWindow = () => {
    if (onOpenWindow) onOpenWindow();
    else window.open(artifactShareUrl(roomId, artifactId, version), "_blank", "noopener,noreferrer");
  };
  const refreshed = () => {
    invalidateArtifacts(roomId, artifactId);
    onVersion(undefined);
  };
  const restore = async () => {
    setBusy(true);
    try {
      const current = artifact.currentVersion;
      const bytes = blob ?? (await loadVersionBlob(roomId, artifact, shown, meta));
      const summary = `Restored v${shown}`;
      const res =
        meta?.storage === "blob"
          ? await api.publishArtifactFile(roomId, artifactId, { file: bytes, fileName: artifact.fileName || file.name, baseVersion: current, summary })
          : await api.publishArtifactVersion(roomId, artifactId, { baseVersion: current, content: await bytes.text(), summary });
      toast.success(`Restored v${shown} as v${res.artifact.currentVersion}`);
      refreshed();
    } catch (e) {
      toast.error(errorText(e, "Restore failed"));
    } finally {
      setBusy(false);
    }
  };
  const publish = async () => {
    setBusy(true);
    try {
      await api.publishArtifactVersion(roomId, artifactId, { baseVersion: artifact.currentVersion, content: draft });
      setMode("view");
      refreshed();
    } catch (e) {
      toast.error(errorText(e, "Publish failed"));
    } finally {
      setBusy(false);
    }
  };
  const startCompare = () => {
    if (mode === "compare") {
      setMode("view");
      return;
    }
    setCompareWith(versions.find((v) => v < shown) ?? versions.find((v) => v !== shown) ?? null);
    setMode("compare");
  };
  const startEdit = async () => {
    if (mode === "edit") {
      setMode("view");
      return;
    }
    setDraft(blob ? await blob.text() : "");
    setMode("edit");
  };

  return (
    <div className="@container flex h-full min-h-0 flex-col" data-artifact-tab={artifactId}>
      <div className="flex shrink-0 flex-col gap-1 border-b border-[var(--color-border)] px-2 py-1.5">
        <div className="flex min-w-0 items-center gap-1.5">
          <Icon size={16} className="ml-1 shrink-0 text-[var(--color-accent)]" aria-hidden />
          <span className="min-w-0 truncate text-sm font-medium text-[var(--color-fg)]" title={artifact.title}>
            {file.name}
          </span>
          <span className="shrink-0 rounded-full border border-[var(--color-border)] px-1.5 text-[10px] uppercase tracking-wide text-[var(--color-fg-subtle)]">
            {fileTypeLabel(facts)}
          </span>
          {meta?.sizeBytes !== undefined && (
            <span className="hidden shrink-0 text-[11px] text-[var(--color-fg-subtle)] tabular @xs:inline">{formatBytes(meta.sizeBytes)}</span>
          )}
          {changedMeanwhile && (
            <span
              className="shrink-0 rounded-full border border-[var(--color-border)] px-1.5 text-[10px] text-[var(--color-fg-muted)]"
              title={`This edit started from v${editedFrom}; v${meta?.parentVersion} landed while it ran. Compare them to see what each changed.`}
            >
              Edited from v{editedFrom} · v{meta?.parentVersion} changed meanwhile
            </span>
          )}
          {updated !== null && (
            <span role="status" className="animate-in shrink-0 rounded-full bg-[var(--color-accent-soft)] px-1.5 text-[10px] font-medium text-[var(--color-accent)]">
              Updated · v{updated}
            </span>
          )}
          <span className="ml-auto flex shrink-0 items-center">
            {canAsk && (
              <button
                type="button"
                className={cn(ACTION, asking && "bg-[var(--color-elevated)] text-[var(--color-fg)]")}
                onClick={() => setAsking((v) => !v)}
                title="Ask AI to change this file"
                aria-label="Ask AI to change this file"
                aria-pressed={asking}
              >
                <SparkleIcon size={16} />
              </button>
            )}
            <button type="button" className={ACTION} onClick={() => void download()} title="Download" aria-label="Download">
              <DownloadSimpleIcon size={16} />
            </button>
            <button type="button" className={ACTION} onClick={copyLink} title="Copy link" aria-label="Copy link to this deliverable">
              <LinkIcon size={16} />
            </button>
            <button type="button" className={ACTION} onClick={() => setSharingOpen(true)} title="Share" aria-label="Share a link" data-tour="share">
              <ShareNetworkIcon size={16} />
            </button>
            {onOpenWindow !== null && (
              <button type="button" className={ACTION} onClick={openWindow} title="Open in new window" aria-label="Open in new window">
                <ArrowSquareOutIcon size={16} />
              </button>
            )}
            {onToggleFullscreen && (
              <button
                type="button"
                className={ACTION}
                onClick={onToggleFullscreen}
                title={fullscreen ? "Exit fullscreen (Esc)" : "Fullscreen"}
                aria-label={fullscreen ? "Exit fullscreen" : "Fullscreen"}
                aria-pressed={!!fullscreen}
              >
                {fullscreen ? <CornersInIcon size={16} /> : <CornersOutIcon size={16} />}
              </button>
            )}
          </span>
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-1">
          <Select
            aria-label="Version"
            value={shown}
            onChange={(e) => {
              const v = Number(e.target.value);
              onVersion(v === artifact.currentVersion ? undefined : v);
              if (mode === "edit") setMode("view");
            }}
            className={SELECT}
          >
            {versions.map((v) => (
              <option key={v} value={v}>
                {versionLabel(detail, v, nameOf)}
              </option>
            ))}
          </Select>
          {versions.length > 1 && comparer && (
            <button type="button" className={cn(CHIP, mode === "compare" && "bg-[var(--color-elevated)] text-[var(--color-fg)]")} onClick={startCompare} aria-pressed={mode === "compare"}>
              <GitDiffIcon size={14} /> Compare
            </button>
          )}
          {mode === "compare" && (
            <Select
              aria-label="Compare against"
              value={compareWith ?? ""}
              onChange={(e) => setCompareWith(Number(e.target.value))}
              className={SELECT}
            >
              {versions
                .filter((v) => v !== shown)
                .map((v) => (
                  <option key={v} value={v}>
                    against {versionLabel(detail, v, nameOf)}
                  </option>
                ))}
            </Select>
          )}
          {!latest && (
            <button type="button" className={CHIP} disabled={busy} onClick={() => void restore()} title="Publish this version's content as a new version">
              <ArrowCounterClockwiseIcon size={14} /> Restore as new version
            </button>
          )}
          {editable && (
            <button type="button" className={cn(CHIP, mode === "edit" && "bg-[var(--color-elevated)] text-[var(--color-fg)]")} onClick={() => void startEdit()} aria-pressed={mode === "edit"}>
              <PencilSimpleIcon size={14} /> Edit
            </button>
          )}
          {!runsSite && (
            <LocalSaveChip artifactId={artifactId} version={shown} load={async () => blob ?? (await loadVersionBlob(roomId, artifact, shown, meta))} className={CHIP} />
          )}
          {mode === "view" && <ViewerToolbar items={tools.items} onAction={tools.emit} className="ml-auto" />}
          {mode === "view" && onViewer && (
            <OpenWithMenu file={file} currentViewerId={viewer?.id} onChoose={(id) => onViewer(id ?? undefined)} className={tools.items.length === 0 ? "ml-auto" : undefined} />
          )}
        </div>
      </div>
      {artifact.isApp && <AppDataLine roomId={roomId} artifactId={artifactId} appName={file.name} />}
      <div className="flex min-h-0 flex-1 flex-col @2xl:flex-row">
      <div className="relative min-h-0 flex-1 overflow-hidden" role="region" aria-label={file.name}>
        {mode === "edit" ? (
          <div className="flex h-full flex-col gap-2 p-2">
            <textarea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              spellCheck={false}
              aria-label={`Edit ${file.name}`}
              className="min-h-0 flex-1 resize-none rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] p-2 font-mono text-[12px] leading-5 text-[var(--color-fg)]"
            />
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => void publish()}
                disabled={busy}
                className="tactile flex items-center gap-1 rounded-[var(--radius)] bg-[var(--color-accent)] px-3 py-1.5 text-xs font-medium text-[var(--color-accent-fg,#04120c)] disabled:opacity-50"
              >
                <CheckIcon size={14} /> Publish v{artifact.currentVersion + 1}
              </button>
              <button type="button" onClick={() => setMode("view")} className={CHIP}>
                Cancel
              </button>
              <span className="ml-auto text-[11px] text-[var(--color-fg-subtle)] tabular">{draft.length.toLocaleString()} chars</span>
            </div>
          </div>
        ) : mode === "compare" ? (
          !comparer || compareWith === null ? (
            <p className="p-4 text-sm text-[var(--color-fg-subtle)]">These versions cannot be compared. Pick a version above to view it.</p>
          ) : (
            <CompareView roomId={roomId} artifact={artifact} detail={detail} comparer={comparer} prevVersion={compareWith} currVersion={shown} />
          )
        ) : blobError ? (
          <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Could not load this file: {blobError}</p>
        ) : !blob && !runsSite ? (
          <p className="p-4 text-sm text-[var(--color-fg-subtle)]">Loading preview…</p>
        ) : (
          <ArtifactBody
            roomId={roomId}
            actor={actor}
            artifactId={artifactId}
            file={file}
            blob={blob}
            site={runsSite ? site : null}
            viewer={viewer}
            onDownload={() => void download()}
            onToolbar={onToolbar}
            onLocalState={onLocalState ? (st) => onLocalState(artifactId, st) : undefined}
            remoteState={liveStates?.[artifactId]}
            onSelection={setSelection}
          />
        )}
      </div>
      {asking && ask && canAsk && (
        <FileAskPanel
          ask={ask}
          fileRef={{ artifactId, version: shown, name: file.name, ...(selection ? { selection: requestSelection(selection) } : {}) }}
          file={file}
          pick={pick}
          onClearSelection={() => setSelection(null)}
          onClose={() => setAsking(false)}
        />
      )}
      </div>
      <ShareDialog
        open={sharingOpen}
        onClose={() => setSharingOpen(false)}
        roomId={roomId}
        artifactId={artifactId}
        fileName={file.name}
        version={shown}
        kind={artifact?.isApp ? "app" : facts && isSite(facts) ? "site" : "file"}
      />
    </div>
  );
}
