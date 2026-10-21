import { useMemo, useState } from "react";
import { MagnifyingGlassIcon, SquaresFourIcon, StarIcon } from "@phosphor-icons/react";
import { cn } from "@/lib/cn";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { errorText } from "@/lib/errors";
import { galleryWorks, GALLERY_TABS, TYPE_FILTERS, withFavorite, type GalleryTab, type TypeFilter } from "@/lib/workGallery";
import { workGalleryApi } from "@/lib/workGalleryApi";
import { Button } from "@/ui/Button";
import { Card, Skeleton } from "@/ui/Card";
import { Input } from "@/ui/Input";
import { EmptyState } from "@/ui/Page";
import { LoadFailed } from "@/ui/QueryState";
import { useToast } from "@/ui/Toast";
import { AppCover, componentCover } from "@/views/workbench/AppCover";
import { useInView } from "@/views/workbench/FileCard";
import { WorkDetailDialog } from "./WorkDetailDialog";
import { FavoriteStar, useWorks, WorkAuthorName, WorkTypeBadge } from "./WorkParts";

// WorkGallery is Plaza → "Works" (app gallery PRD §7, works PRD §2): the
// work components the member sees (the content listing, kind work: their
// own, shared copies, their organization's and the platform's) — apps,
// images, videos, documents, slides, sites and any other file — as a grid
// of cards: the cover (the work's own, an image work's image, else the
// generated one), a type badge, the name, the author and a star. A row of
// type filters sits beside the category tabs, with a search over name,
// description and tags, and "My favorites". A card opens the work's
// detail: the work itself, its README and recipe, Make similar, and for an
// app Try demo and Use this app. Three columns on a desktop, two on a
// tablet, one on a phone.

export function WorkGallery({
  onOpenRoom,
}: {
  readonly onOpenRoom: (roomId: string, artifactId?: string) => void;
}) {
  const toast = useToast();
  const list = useWorks();
  const [tab, setTab] = useState<GalleryTab>("all");
  const [type, setType] = useState<TypeFilter>("all");
  const [query, setQuery] = useState("");
  const [favoritesOnly, setFavoritesOnly] = useState(false);
  // Stars toggled here, over the listing's answer.
  const [stars, setStars] = useState<ReadonlyMap<string, boolean>>(new Map());
  const [openId, setOpenId] = useState<string | null>(null);

  const all = useMemo(() => (list.data ?? []).map((c) => (stars.has(c.id) ? withFavorite(c, stars.get(c.id)!) : c)), [list.data, stars]);
  const works = useMemo(() => galleryWorks(all, { tab, type, query, favoritesOnly }), [all, tab, type, query, favoritesOnly]);
  const open = all.find((c) => c.id === openId) ?? null;
  const filtered = tab !== "all" || type !== "all" || query.trim() !== "" || favoritesOnly;

  const toggleStar = async (c: ContentComponent) => {
    const on = !c.favorite;
    setStars((m) => new Map(m).set(c.id, on));
    try {
      await workGalleryApi.favorite(c.id, on);
    } catch (e) {
      setStars((m) => new Map(m).set(c.id, !on));
      toast.error(errorText(e, on ? "Could not add it to your favorites" : "Could not remove it from your favorites"));
    }
  };

  return (
    <section aria-label="Works" className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-0 flex-[1_1_16rem] sm:max-w-md">
          <MagnifyingGlassIcon size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
          <Input className="pl-9" placeholder="Search works…" aria-label="Search works" value={query} onChange={(e) => setQuery(e.target.value)} />
        </div>
        <Button variant={favoritesOnly ? "primary" : "outline"} aria-pressed={favoritesOnly} onClick={() => setFavoritesOnly((v) => !v)}>
          <StarIcon size={15} weight={favoritesOnly ? "fill" : "regular"} />
          My favorites
        </Button>
      </div>
      <div className="flex flex-col gap-2">
        <FilterRow label="Types" options={TYPE_FILTERS} value={type} onChange={setType} />
        <FilterRow label="Categories" options={GALLERY_TABS} value={tab} onChange={setTab} />
      </div>

      {list.error && !list.data ? (
        <LoadFailed message={errorText(list.error, "Could not load the works")} onRetry={() => void list.refetch()} />
      ) : !list.data ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3" role="status" aria-label="Loading works">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="aspect-[16/13]" />
          ))}
        </div>
      ) : works.length === 0 ? (
        <EmptyState
          icon={<SquaresFourIcon size={26} />}
          title={filtered ? "No works match" : "No works yet"}
          hint={
            filtered
              ? favoritesOnly && !query.trim() && tab === "all" && type === "all"
                ? "Star a work to find it here."
                : "Try another type, category or search, or clear the filters."
              : "Publish a deliverable from its card in a room (Publish to gallery), and it appears here."
          }
          action={
            filtered ? (
              <Button
                variant="outline"
                onClick={() => {
                  setTab("all");
                  setType("all");
                  setQuery("");
                  setFavoritesOnly(false);
                }}
              >
                Clear filters
              </Button>
            ) : undefined
          }
        />
      ) : (
        <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3" aria-label="Works">
          {works.map((c) => (
            <li key={c.id} className="min-w-0">
              <WorkCard work={c} onOpen={() => setOpenId(c.id)} onStar={() => void toggleStar(c)} />
            </li>
          ))}
        </ul>
      )}
      <WorkDetailDialog
        work={open}
        onClose={() => setOpenId(null)}
        onStar={() => open && void toggleStar(open)}
        onUsed={(roomId, artifactId) => {
          setOpenId(null);
          onOpenRoom(roomId, artifactId);
        }}
        onDrafted={(roomId) => {
          setOpenId(null);
          onOpenRoom(roomId);
        }}
      />
    </section>
  );
}

/** FilterRow is one row of chips (the types, the categories). */
function FilterRow<T extends string>({
  label,
  options,
  value,
  onChange,
}: {
  readonly label: string;
  readonly options: readonly { readonly value: T; readonly label: string }[];
  readonly value: T;
  readonly onChange: (v: T) => void;
}) {
  return (
    <div role="tablist" aria-label={label} className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1 [scrollbar-width:none]">
      {options.map((t) => (
        <button
          key={t.value}
          type="button"
          role="tab"
          aria-selected={value === t.value}
          onClick={() => onChange(t.value)}
          className={cn(
            "tactile shrink-0 rounded-full border px-3 py-1 text-sm",
            value === t.value
              ? "border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] text-[var(--color-accent)]"
              : "border-[var(--color-border)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
          )}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

/** WorkCard is one work in a grid: the gallery's, and the Library's. */
export function WorkCard({ work, onOpen, onStar }: { readonly work: ContentComponent; readonly onOpen: () => void; readonly onStar?: () => void }) {
  const [ref, visible] = useInView<HTMLButtonElement>();
  const label = contentLabel(work);
  return (
    <Card className="group/app relative flex h-full min-w-0 flex-col overflow-hidden">
      <button
        ref={ref}
        type="button"
        onClick={onOpen}
        aria-label={`Open ${label}`}
        className="tactile flex min-w-0 flex-1 flex-col text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-accent)]/60"
      >
        <span className="relative block aspect-[16/10] w-full overflow-hidden border-b border-[var(--color-border)] bg-[var(--color-surface-2)]">
          <AppCover
            seed={work.id}
            name={label}
            cover={componentCover(work.coverUrl)}
            enabled={visible}
            className="transition-transform duration-300 group-hover/app:scale-[1.03]"
          />
          <WorkTypeBadge work={work} className="absolute left-2 top-2 shadow-[var(--shadow-pop)]" />
        </span>
        <span className="flex min-w-0 flex-col gap-0.5 px-3 pb-3 pr-16 pt-2.5">
          <span className="truncate font-medium text-[var(--color-fg)]" title={label}>
            {label}
          </span>
          <span className="truncate text-xs text-[var(--color-fg-subtle)]">
            <WorkAuthorName work={work} />
          </span>
        </span>
      </button>
      {onStar && <FavoriteStar work={work} onToggle={onStar} className="absolute bottom-2 right-2" />}
    </Card>
  );
}
