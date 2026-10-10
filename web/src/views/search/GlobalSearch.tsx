import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { BooksIcon, ChatTextIcon, FileTextIcon, MagnifyingGlassIcon } from "@phosphor-icons/react";
import { api, type SearchResult } from "@/lib/api";
import { cn } from "@/lib/cn";
import { relativeTime } from "@/lib/format";
import {
  groupResults,
  highlightParts,
  messageJumps,
  OPEN_SEARCH_EVENT,
  queueLibraryQuery,
  SEARCH_KIND_LABELS,
} from "@/lib/team";
import { useModalLayer } from "@/ui/useModalLayer";
import { ActorName } from "@/views/UserName";

const MIN_QUERY = 2;

const KIND_ICONS = { message: ChatTextIcon, artifact: FileTextIcon, prompt: BooksIcon };

function Highlighted({ text, query }: { text: string; query: string }) {
  return (
    <>
      {highlightParts(text, query).map((p, i) =>
        p.match ? (
          <mark key={i} className="rounded-sm bg-[var(--color-accent-soft)] px-0.5 text-[var(--color-fg)]">
            {p.text}
          </mark>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </>
  );
}

// GlobalSearch is the Cmd/Ctrl+K search (F7) over messages, room documents
// and library prompts across every room the member belongs to (the server
// resolves membership). Results are grouped by kind and keyboard navigable;
// choosing a message opens its room scrolled to it.
export function GlobalSearch({ onOpenRoom }: { onOpenRoom: (roomId: string) => void }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [nextBefore, setNextBefore] = useState<number | undefined>();
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const [active, setActive] = useState(0);
  const surfaceRef = useRef<HTMLDivElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);
  const close = useCallback(() => setOpen(false), []);
  useModalLayer(surfaceRef, { open, onEscape: close });

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey) {
        e.preventDefault();
        setOpen((o) => !o);
      }
    };
    const onOpen = () => setOpen(true);
    window.addEventListener("keydown", onKey);
    window.addEventListener(OPEN_SEARCH_EVENT, onOpen);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener(OPEN_SEARCH_EVENT, onOpen);
    };
  }, []);

  // Debounced, latest-wins search.
  const query = q.trim();
  useEffect(() => {
    if (!open || query.length < MIN_QUERY) {
      setResults([]);
      setNextBefore(undefined);
      setFailed(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    const timer = window.setTimeout(() => {
      api
        .search(query)
        .then((r) => {
          if (cancelled) return;
          setResults(r.results ?? []);
          setNextBefore(r.nextBefore);
          setFailed(false);
          setActive(0);
        })
        .catch(() => !cancelled && setFailed(true))
        .finally(() => !cancelled && setLoading(false));
    }, 250);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [open, query]);

  const loadMore = () => {
    if (!nextBefore || loading) return;
    setLoading(true);
    api
      .search(query, { before: nextBefore })
      .then((r) => {
        setResults((cur) => [...cur, ...(r.results ?? [])]);
        setNextBefore(r.nextBefore);
      })
      .catch(() => setFailed(true))
      .finally(() => setLoading(false));
  };

  const groups = useMemo(() => groupResults(results), [results]);
  const flat = useMemo(() => groups.flatMap((g) => g.items), [groups]);

  const choose = (r: SearchResult) => {
    setOpen(false);
    if (r.kind === "prompt") {
      queueLibraryQuery(r.title ?? "");
      window.location.hash = "#/library";
      return;
    }
    if (!r.roomId) return;
    if (r.kind === "message" && r.seq) messageJumps.queue(r.roomId, { seq: r.seq, channelId: r.channelId || undefined });
    onOpenRoom(r.roomId);
  };

  useEffect(() => {
    listRef.current?.querySelector(`[data-result-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  if (!open) return null;
  let index = -1;
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center p-4 pt-[12vh]">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm animate-in" onClick={close} />
      <div
        ref={surfaceRef}
        role="dialog"
        aria-modal="true"
        aria-label="Search"
        className="animate-in relative flex max-h-[70vh] w-full max-w-2xl flex-col overflow-hidden rounded-[var(--radius-xl)] border border-[var(--color-border-strong)] bg-[var(--color-surface)] shadow-[var(--shadow-diffuse)]"
      >
        <div className="flex items-center gap-2 border-b border-[var(--color-border)] px-4">
          <MagnifyingGlassIcon size={18} className="shrink-0 text-[var(--color-fg-subtle)]" />
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setActive((a) => Math.min(flat.length - 1, a + 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive((a) => Math.max(0, a - 1));
              } else if (e.key === "Enter" && flat[active]) {
                e.preventDefault();
                choose(flat[active]);
              }
            }}
            placeholder="Search messages, documents and prompts…"
            aria-label="Search messages, documents and prompts"
            className="h-12 min-w-0 flex-1 bg-transparent text-sm text-[var(--color-fg)] placeholder:text-[var(--color-fg-subtle)] focus:outline-none"
          />
          <kbd className="hidden rounded border border-[var(--color-border)] px-1.5 text-[0.65rem] text-[var(--color-fg-subtle)] sm:inline">Esc</kbd>
        </div>
        <div ref={listRef} className="min-h-0 flex-1 overflow-y-auto py-2">
          {query.length < MIN_QUERY ? (
            <p className="px-4 py-6 text-center text-sm text-[var(--color-fg-subtle)]">Type at least 2 characters to search your rooms.</p>
          ) : failed ? (
            <p className="px-4 py-6 text-center text-sm text-[var(--color-danger)]">Search is unavailable right now.</p>
          ) : flat.length === 0 ? (
            <p className="px-4 py-6 text-center text-sm text-[var(--color-fg-subtle)]">{loading ? "Searching…" : `No results for “${query}”.`}</p>
          ) : (
            groups.map((g) => (
              <div key={g.kind} className="mb-1">
                <div className="px-4 pb-1 pt-2 text-[0.68rem] font-semibold uppercase tracking-wide text-[var(--color-fg-subtle)]">
                  {SEARCH_KIND_LABELS[g.kind]}
                </div>
                {g.items.map((r) => {
                  index += 1;
                  const i = index;
                  const Icon = KIND_ICONS[r.kind];
                  return (
                    <button
                      key={`${r.kind}:${r.roomId ?? ""}:${r.seq ?? r.artifactId ?? r.promptId}:${i}`}
                      type="button"
                      data-result-index={i}
                      onMouseMove={() => setActive(i)}
                      onClick={() => choose(r)}
                      className={cn(
                        "flex w-full items-start gap-3 px-4 py-2 text-left",
                        i === active ? "bg-[var(--color-surface-2)]" : "hover:bg-[var(--color-surface-2)]",
                      )}
                    >
                      <Icon size={16} className="mt-0.5 shrink-0 text-[var(--color-fg-subtle)]" />
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-2 text-xs text-[var(--color-fg-subtle)]">
                          <span className="truncate font-medium text-[var(--color-fg-muted)]">
                            {r.kind === "prompt" ? r.title : r.roomName || r.roomId}
                          </span>
                          {r.channelKind === "people" && (
                            <span
                              title="People chat — agents can't see it"
                              className="shrink-0 rounded-full border border-[var(--color-border)] px-1.5 text-[10px] uppercase tracking-wide"
                            >
                              People chat
                            </span>
                          )}
                          {r.author && (
                            <span className="truncate">
                              · <ActorName actor={r.author} />
                            </span>
                          )}
                          <span className="ml-auto shrink-0">{relativeTime(r.atMs)}</span>
                        </span>
                        <span className="mt-0.5 line-clamp-2 break-words text-sm text-[var(--color-fg)]">
                          <Highlighted text={r.snippet} query={query} />
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>
            ))
          )}
          {nextBefore && flat.length > 0 && (
            <div className="flex justify-center py-2">
              <button
                type="button"
                onClick={loadMore}
                disabled={loading}
                className="tactile text-xs text-[var(--color-accent)] hover:underline disabled:opacity-50"
              >
                {loading ? "Loading…" : "Load older messages"}
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
