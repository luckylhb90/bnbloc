import { useCallback, useEffect, useMemo, useState } from "react";
import { Select } from "@/ui/Select";
import { FileCodeIcon, FileDocIcon, ListIcon, MagnifyingGlassIcon, SquaresFourIcon, XIcon } from "@phosphor-icons/react";
import { api, ApiError, type Artifact, type ArtifactListFilter } from "@/lib/api";
import { fileCategory, type FileCategory } from "@/lib/artifactFiles";
import { cn } from "@/lib/cn";
import { useToast } from "@/ui/Toast";
import { FileCard, FileRow } from "./FileCard";
import { versionFacts } from "./artifactData";
import { useDisplayNames } from "@/lib/useDisplayName";

// DeliverablesView: every artifact of the room as a filterable grid or list
// (type, author, date, search; optionally only one message's deliverables).
// Server-side filters (q, author, sourceSeq, mime) narrow each page; the
// same filters also apply client-side, so a page from a server that ignores
// one still shows only matching items. Type and date are client-side.

type TypeFilter = "all" | "images" | "documents" | "code" | "tables" | "web" | "media" | "other";
type DateFilter = "any" | "day" | "week" | "month";
type Layout = "grid" | "list";

const TYPES: readonly { value: TypeFilter; label: string; categories: readonly FileCategory[] }[] = [
  { value: "all", label: "All types", categories: [] },
  { value: "images", label: "Images", categories: ["image"] },
  { value: "documents", label: "Documents", categories: ["markdown", "pdf", "text"] },
  { value: "code", label: "Code", categories: ["code", "json"] },
  { value: "tables", label: "Tables", categories: ["table"] },
  { value: "web", label: "Web & apps", categories: ["web"] },
  { value: "media", label: "Audio & video", categories: ["audio", "video"] },
  { value: "other", label: "Other files", categories: ["archive", "file"] },
];
const DATES: readonly { value: DateFilter; label: string; ms: number }[] = [
  { value: "any", label: "Any time", ms: 0 },
  { value: "day", label: "Last 24 hours", ms: 86_400_000 },
  { value: "week", label: "Last 7 days", ms: 7 * 86_400_000 },
  { value: "month", label: "Last 30 days", ms: 30 * 86_400_000 },
];
const PAGE_SIZE = 60;
const LAYOUT_KEY = "bnbloc.deliverables.layout";

function readLayout(): Layout {
  try {
    return localStorage.getItem(LAYOUT_KEY) === "list" ? "list" : "grid";
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
  const [items, setItems] = useState<Artifact[] | null>(null);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const t = window.setTimeout(() => setQ(search.trim()), 250);
    return () => window.clearTimeout(t);
  }, [search]);

  const filter = useMemo<ArtifactListFilter>(
    () => ({ q: q || undefined, author: author || undefined, sourceSeq, mime: type === "images" ? "image/*" : undefined, limit: PAGE_SIZE }),
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
        setFailed(false);
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, filter, refreshSignal]);

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
    } catch {
      toast.error("Could not load more deliverables");
    } finally {
      setLoadingMore(false);
    }
  }, [roomId, filter, cursor, toast]);

  const authors = useMemo(() => [...new Set((items ?? []).map((a) => a.createdBy))].sort(), [items]);
  const nameOf = useDisplayNames(authors);
  const shown = useMemo(() => {
    const categories = TYPES.find((t) => t.value === type)?.categories ?? [];
    const since = DATES.find((d) => d.value === date)?.ms ?? 0;
    const needle = q.toLowerCase();
    return (items ?? []).filter(
      (a) =>
        (categories.length === 0 || categories.includes(fileCategory(versionFacts(a)))) &&
        (!author || a.createdBy === author) &&
        (!since || Date.now() - a.updatedAtMs <= since) &&
        (!needle || a.title.toLowerCase().includes(needle) || (a.fileName ?? "").toLowerCase().includes(needle)),
    );
  }, [items, type, author, date, q]);

  const switchLayout = (next: Layout) => {
    setLayout(next);
    try {
      localStorage.setItem(LAYOUT_KEY, next);
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
      toast.error(e instanceof ApiError && e.message ? e.message : "Could not create the file");
    } finally {
      setBusy(false);
    }
  };

  const filtered = !!(q || author || type !== "all" || date !== "any");
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
            {TYPES.map((t) => (
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
            {DATES.map((d) => (
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
        {failed && items === null ? (
          <p className="py-6 text-center text-xs text-[var(--color-fg-subtle)]">Could not load deliverables.</p>
        ) : items === null ? (
          <p className="py-6 text-center text-xs text-[var(--color-fg-subtle)]">Loading…</p>
        ) : shown.length === 0 ? (
          <p className="py-6 text-center text-xs text-[var(--color-fg-subtle)]">
            {filtered || sourceSeq !== undefined
              ? "No deliverables match these filters."
              : "No deliverables yet. Files agents produce appear here; members can add documents and code."}
          </p>
        ) : layout === "grid" ? (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(8.5rem,1fr))] gap-2">
            {shown.map((a) => (
              <FileCard key={a.artifactId} roomId={roomId} artifactId={a.artifactId} onOpen={onOpen} />
            ))}
          </div>
        ) : (
          <div className="flex flex-col">
            {shown.map((a) => (
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
