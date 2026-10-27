import { useCallback, useEffect, useMemo, useState } from "react";
import { Select } from "@/ui/Select";
import { CaretRightIcon, FileCodeIcon, FileDocIcon, FolderIcon, FolderSimpleIcon, ListIcon, MagnifyingGlassIcon, SquaresFourIcon, XIcon } from "@phosphor-icons/react";
import { api, type Artifact, type ArtifactListFilter } from "@/lib/api";
import { cn } from "@/lib/cn";
import { useToast } from "@/ui/Toast";
import { LoadFailed } from "@/ui/QueryState";
import { FileCard, FileRow } from "./FileCard";
import {
  DATE_FILTERS,
  TYPE_FILTERS,
  deliverableFiltered,
  deliverableMatches,
  deliverableQuery,
  type DateFilter,
  type DeliverableFilter,
  type TypeFilter,
} from "./deliverableFilters";
import { useDisplayNames } from "@/lib/useDisplayName";
import { errorText } from "@/lib/errors";
import { StorageKey, readStored, writeStored } from "@/lib/storageKeys";
import { folderContents, folderCrumbs, type FolderEntry } from "./deliverableFolders";

// DeliverablesView: every artifact of the room as a filterable grid or list
// (type — "Apps" among them —, author, date, search; optionally only one
// message's deliverables). Server-side filters (q, author, sourceSeq, mime,
// apps) narrow each page; the same filters also apply client-side
// (deliverableFilters.ts), so a page from a server that ignores one still
// shows only matching items. The other types and the date are client-side.
// By folder (the default), the deliverables' paths are folders to open
// (deliverableFolders.ts); a search or a filter shows every match, flat.

type Layout = "grid" | "list";

const PAGE_SIZE = 60;
const LAYOUT_KEY = StorageKey.deliverablesLayout;

function readFolders(): boolean {
  try {
    return readStored(StorageKey.deliverablesFolders) !== "off";
  } catch {
    return true;
  }
}

function readLayout(): Layout {
  try {
    return readStored(LAYOUT_KEY) === "list" ? "list" : "grid";
  } catch {
    return "grid";
  }
}

const SELECT =
  "h-8 min-w-0 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 text-xs text-[var(--color-fg)]";

export function DeliverablesView({
  roomId,
  channelId,
  refreshSignal,
  sourceSeq,
  onClearSourceSeq,
  onOpen,
  allowCreate = false,
}: {
  readonly roomId: string;
  readonly channelId?: string | null;
  readonly refreshSignal: number;
  // Only the deliverables of this message (a chat card's "View all").
  readonly sourceSeq?: number;
  readonly onClearSourceSeq?: () => void;
  readonly onOpen: (artifactId: string) => void;
  // The Files panel lets members start a document or code file here.
  readonly allowCreate?: boolean;
}) {
  const toast = useToast();
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  const [type, setType] = useState<TypeFilter>("all");
  const [author, setAuthor] = useState("");
  const [date, setDate] = useState<DateFilter>("any");
  const [layout, setLayout] = useState<Layout>(readLayout);
  const [byFolder, setByFolder] = useState<boolean>(readFolders);
  const [folder, setFolder] = useState("");
  useEffect(() => setFolder(""), [roomId]);
  const [items, setItems] = useState<Artifact[] | null>(null);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [failed, setFailed] = useState<unknown>(null);
  const [reloads, setReloads] = useState(0);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const t = window.setTimeout(() => setQ(search.trim()), 250);
    return () => window.clearTimeout(t);
  }, [search]);

  const shownFilter = useMemo<DeliverableFilter>(() => ({ type, author, date, q }), [type, author, date, q]);
  const filter = useMemo<ArtifactListFilter>(
    () => ({ ...deliverableQuery({ type, author, date: "any", q }), sourceSeq, limit: PAGE_SIZE }),
    [q, author, sourceSeq, type],
  );

  useEffect(() => {
    let cancelled = false;
    api
      .artifacts(roomId, filter)
      .then((res) => {
        if (cancelled) return;
        setItems(res.artifacts ?? []);
        setCursor(res.nextCursor || undefined);
        setFailed(null);
      })
      .catch((e: unknown) => {
        if (!cancelled) setFailed(e ?? new Error("load failed"));
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, filter, refreshSignal, reloads]);

  const loadMore = useCallback(async () => {
    if (!cursor) return;
    setLoadingMore(true);
    try {
      const res = await api.artifacts(roomId, { ...filter, cursor });
      setItems((cur) => {
        const seen = new Set((cur ?? []).map((a) => a.artifactId));
        return [...(cur ?? []), ...(res.artifacts ?? []).filter((a) => !seen.has(a.artifactId))];
      });
      setCursor(res.nextCursor || undefined);
    } catch (e) {
      toast.error(errorText(e, "Could not load more deliverables"));
    } finally {
      setLoadingMore(false);
    }
  }, [roomId, filter, cursor, toast]);

  const authors = useMemo(() => [...new Set((items ?? []).map((a) => a.createdBy))].sort(), [items]);
  const nameOf = useDisplayNames(authors);
  const shown = useMemo(() => {
    const now = Date.now();
    return (items ?? []).filter((a) => deliverableMatches(a, shownFilter, now));
  }, [items, shownFilter]);

  const switchLayout = (next: Layout) => {
    setLayout(next);
    try {
      writeStored(LAYOUT_KEY, next);
    } catch {
      /* preference only */
    }
  };

  const create = async (kind: "markdown" | "code") => {
    const title = window.prompt(kind === "markdown" ? "Document title" : "File name");
    if (!title?.trim()) return;
    setBusy(true);
    try {
      const res = await api.createArtifact(roomId, {
        kind,
        title: title.trim(),
        channelId: channelId ?? undefined,
        content: kind === "markdown" ? `# ${title.trim()}\n` : "",
      });
      onOpen(res.artifact.artifactId);
    } catch (e) {
      toast.error(errorText(e, "Could not create the file"));
    } finally {
      setBusy(false);
    }
  };

  const switchFolders = () => {
    const next = !byFolder;
    setByFolder(next);
    setFolder("");
    try {
      writeStored(StorageKey.deliverablesFolders, next ? "on" : "off");
    } catch {
      /* preference only */
    }
  };

  const filtered = deliverableFiltered(shownFilter);
  // decision: docs/prd/2026-10-27-repo-files-and-folders.md (2), a search or
  // a filter shows every match, flat.
  const flat = !byFolder || filtered || sourceSeq !== undefined;
  const view = flat ? { folders: [] as FolderEntry[], files: shown } : folderContents(shown, folder);
  return (
    <div className="@container flex h-full min-h-0 flex-col">
      <div className="flex flex-col gap-2 border-b border-[var(--color-border)] p-3">
        <div className="flex items-center gap-2">
          <span className="text-xs font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">All deliverables</span>
          {items && <span className="text-xs text-[var(--color-fg-subtle)] tabular">{shown.length}</span>}
          <div className="ml-auto flex items-center gap-1">
            {allowCreate && (
              <>
                <IconButton label="New document" disabled={busy} onClick={() => void create("markdown")}>
                  <FileDocIcon size={16} />
                </IconButton>
                <IconButton label="New code file" disabled={busy} onClick={() => void create("code")}>
                  <FileCodeIcon size={16} />
                </IconButton>
              </>
            )}
            <IconButton label={byFolder ? "Show all files, without folders" : "Show by folder"} pressed={byFolder} onClick={switchFolders}>
              <FolderSimpleIcon size={15} />
            </IconButton>
            <div className="flex rounded-[var(--radius)] border border-[var(--color-border)] p-0.5" role="group" aria-label="Layout">
              <IconButton label="Grid" pressed={layout === "grid"} onClick={() => switchLayout("grid")}>
                <SquaresFourIcon size={15} />
              </IconButton>
              <IconButton label="List" pressed={layout === "list"} onClick={() => switchLayout("list")}>
                <ListIcon size={15} />
              </IconButton>
            </div>
          </div>
        </div>
        <label className="relative block">
          <MagnifyingGlassIcon size={14} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
          <input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search by name"
            aria-label="Search deliverables"
            className="h-8 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] pl-7 pr-2 text-xs text-[var(--color-fg)] placeholder:text-[var(--color-fg-subtle)]"
          />
        </label>
        <div className="grid grid-cols-3 gap-1.5">
          <Select aria-label="Type" value={type} onChange={(e) => setType(e.target.value as TypeFilter)} className={SELECT}>
            {TYPE_FILTERS.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </Select>
          <Select aria-label="Author" value={author} onChange={(e) => setAuthor(e.target.value)} className={SELECT}>
            <option value="">Anyone</option>
            {authors.map((a) => (
              <option key={a} value={a}>
                {nameOf(a)}
              </option>
            ))}
          </Select>
          <Select aria-label="Date" value={date} onChange={(e) => setDate(e.target.value as DateFilter)} className={SELECT}>
            {DATE_FILTERS.map((d) => (
              <option key={d.value} value={d.value}>
                {d.label}
              </option>
            ))}
          </Select>
        </div>
        {sourceSeq !== undefined && (
          <span className="inline-flex w-fit items-center gap-1 rounded-full border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] py-0.5 pl-2 pr-1 text-[11px] text-[var(--color-accent)]">
            From one message
            {onClearSourceSeq && (
              <button
                type="button"
                onClick={onClearSourceSeq}
                className="grid size-4 place-items-center rounded-full hover:bg-[var(--color-accent)]/20"
                aria-label="Show deliverables from every message"
                title="Show all"
              >
                <XIcon size={10} />
              </button>
            )}
          </span>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-3">
        {!flat && folder !== "" && <FolderCrumbs folder={folder} onGo={setFolder} />}
        {failed !== null && items === null ? (
          <LoadFailed message={errorText(failed, "Could not load the deliverables")} onRetry={() => setReloads((n) => n + 1)} />
        ) : items === null ? (
          <p role="status" className="py-6 text-center text-xs text-[var(--color-fg-subtle)]">
            Loading…
          </p>
        ) : !flat && folder !== "" && view.folders.length === 0 && view.files.length === 0 ? (
          <p className="py-6 text-center text-xs text-[var(--color-fg-subtle)]">This folder has no deliverables.</p>
        ) : shown.length === 0 ? (
          <p className="py-6 text-center text-xs text-[var(--color-fg-subtle)]">
            {type === "apps" && !q && !author && date === "any" && sourceSeq === undefined
              ? "No apps in this room yet. Ask an agent to build one: a poll, a sign-up sheet or a survey."
              : filtered || sourceSeq !== undefined
                ? "No deliverables match these filters."
                : "No deliverables yet. Files agents produce appear here; members can add documents and code."}
          </p>
        ) : layout === "grid" ? (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] gap-2">
            {view.folders.map((f) => (
              <FolderCard key={f.path} folder={f} onOpen={setFolder} />
            ))}
            {view.files.map((a) => (
              <FileCard key={a.artifactId} roomId={roomId} artifactId={a.artifactId} onOpen={onOpen} />
            ))}
          </div>
        ) : (
          <div className="flex flex-col">
            {view.folders.map((f) => (
              <FolderRow key={f.path} folder={f} onOpen={setFolder} />
            ))}
            {view.files.map((a) => (
              <FileRow key={a.artifactId} artifact={a} onOpen={onOpen} />
            ))}
          </div>
        )}
        {cursor && (
          <div className="mt-3 flex justify-center">
            <button
              type="button"
              disabled={loadingMore}
              onClick={() => void loadMore()}
              className="tactile rounded-[var(--radius)] px-3 py-1.5 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] disabled:opacity-50"
            >
              {loadingMore ? "Loading…" : "Load more"}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}

function countLabel(n: number): string {
  return n === 1 ? "1 file" : `${n} files`;
}

/** The way back from a folder: the top, then each folder above it. */
function FolderCrumbs({ folder, onGo }: { readonly folder: string; readonly onGo: (path: string) => void }) {
  const crumbs = folderCrumbs(folder);
  return (
    <nav aria-label="Folder" className="mb-2 flex min-w-0 flex-wrap items-center gap-0.5 text-xs">
      <button type="button" onClick={() => onGo("")} className="tactile rounded px-1 py-0.5 text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]">
        All
      </button>
      {crumbs.map((c, i) => (
        <span key={c.path} className="flex min-w-0 items-center gap-0.5">
          <CaretRightIcon size={10} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
          {i === crumbs.length - 1 ? (
            <span aria-current="page" className="truncate px-1 py-0.5 font-medium text-[var(--color-fg)]">
              {c.name}
            </span>
          ) : (
            <button type="button" onClick={() => onGo(c.path)} className="tactile truncate rounded px-1 py-0.5 text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)]">
              {c.name}
            </button>
          )}
        </span>
      ))}
    </nav>
  );
}

function FolderCard({ folder, onOpen }: { readonly folder: FolderEntry; readonly onOpen: (path: string) => void }) {
  return (
    <button
      type="button"
      onClick={() => onOpen(folder.path)}
      title={`Open ${folder.name}`}
      aria-label={`Open folder ${folder.name}, ${countLabel(folder.count)}`}
      className="tactile group flex min-w-0 flex-col overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)] text-left hover:border-[var(--color-border-strong)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
    >
      <span className="grid aspect-[16/10] w-full place-items-center bg-[var(--color-surface)]">
        <FolderIcon size={30} weight="duotone" className="text-[var(--color-fg-subtle)]" aria-hidden />
      </span>
      <span className="flex min-w-0 flex-col gap-0.5 px-2 py-1.5">
        <span className="truncate text-xs font-medium text-[var(--color-fg)]">{folder.name}</span>
        <span className="truncate text-[11px] text-[var(--color-fg-subtle)]">Folder · {countLabel(folder.count)}</span>
      </span>
    </button>
  );
}

function FolderRow({ folder, onOpen }: { readonly folder: FolderEntry; readonly onOpen: (path: string) => void }) {
  return (
    <button
      type="button"
      onClick={() => onOpen(folder.path)}
      aria-label={`Open folder ${folder.name}, ${countLabel(folder.count)}`}
      className="tactile flex w-full min-w-0 items-center gap-2.5 rounded-[var(--radius)] px-2 py-2 text-left text-sm hover:bg-[var(--color-surface-2)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
    >
      <FolderIcon size={18} weight="duotone" className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
      <span className="min-w-0 flex-1 truncate text-[var(--color-fg)]">{folder.name}</span>
      <span className="shrink-0 text-[11px] text-[var(--color-fg-subtle)] tabular">{countLabel(folder.count)}</span>
    </button>
  );
}

function IconButton({
  label,
  pressed,
  disabled,
  onClick,
  children,
}: {
  readonly label: string;
  readonly pressed?: boolean;
  readonly disabled?: boolean;
  readonly onClick: () => void;
  readonly children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      aria-pressed={pressed}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "tap tactile grid size-7 place-items-center rounded-[calc(var(--radius)-2px)] text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] disabled:opacity-50",
        pressed && "bg-[var(--color-elevated)] text-[var(--color-fg)]",
      )}
    >
      {children}
    </button>
  );
}
