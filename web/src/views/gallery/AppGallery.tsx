import { useMemo, useState } from "react";
import { AppWindowIcon, MagnifyingGlassIcon, StarIcon } from "@phosphor-icons/react";
import { galleryApps, GALLERY_TABS, withFavorite, type GalleryTab } from "@/lib/appGallery";
import { appGalleryApi } from "@/lib/appGalleryApi";
import { useAuth } from "@/lib/auth";
import { cn } from "@/lib/cn";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import { queryKey, useQuery } from "@/lib/useQuery";
import { Button } from "@/ui/Button";
import { Card, Skeleton } from "@/ui/Card";
import { Input } from "@/ui/Input";
import { EmptyState } from "@/ui/Page";
import { LoadFailed } from "@/ui/QueryState";
import { useToast } from "@/ui/Toast";
import { AppCover, componentCover } from "@/views/workbench/AppCover";
import { useInView } from "@/views/workbench/FileCard";
import { AppDetailDialog } from "./AppDetailDialog";
import { AppAuthorName, FavoriteStar, GALLERY_QUERY } from "./AppParts";

// AppGallery is Plaza → "Apps" (docs/prd/2026-10-13-app-gallery.md §7): the
// app components the member sees (the content listing, kind app: their
// own, shared copies, their organization's and the platform's), as a grid
// of cards — the cover (the app's own, else the generated one), the name,
// the author and a star — with category tabs, a search over name,
// description and tags, and "My favorites". A card opens the app's detail:
// its README, Try demo and Use this app. Three columns on a desktop, two
// on a tablet, one on a phone.

export function AppGallery({ onOpenRoom }: { readonly onOpenRoom: (roomId: string, artifactId: string) => void }) {
  const { subject, tenant } = useAuth();
  const toast = useToast();
  const list = useQuery(queryKey(GALLERY_QUERY, { subject, tenant }), () => contentApi.list("app").then((r) => r.components ?? []), { staleMs: 30_000 });
  const [tab, setTab] = useState<GalleryTab>("all");
  const [query, setQuery] = useState("");
  const [favoritesOnly, setFavoritesOnly] = useState(false);
  // Stars toggled here, over the listing's answer.
  const [stars, setStars] = useState<ReadonlyMap<string, boolean>>(new Map());
  const [openId, setOpenId] = useState<string | null>(null);

  const all = useMemo(() => (list.data ?? []).map((c) => (stars.has(c.id) ? withFavorite(c, stars.get(c.id)!) : c)), [list.data, stars]);
  const apps = useMemo(() => galleryApps(all, { tab, query, favoritesOnly }), [all, tab, query, favoritesOnly]);
  const open = all.find((c) => c.id === openId) ?? null;
  const filtered = tab !== "all" || query.trim() !== "" || favoritesOnly;

  const toggleStar = async (c: ContentComponent) => {
    const on = !c.favorite;
    setStars((m) => new Map(m).set(c.id, on));
    try {
      await appGalleryApi.favorite(c.id, on);
    } catch (e) {
      setStars((m) => new Map(m).set(c.id, !on));
      toast.error(errorText(e, on ? "Could not add it to your favorites" : "Could not remove it from your favorites"));
    }
  };

  return (
    <section aria-label="Apps" className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-0 flex-[1_1_16rem] sm:max-w-md">
          <MagnifyingGlassIcon size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--color-fg-subtle)]" />
          <Input className="pl-9" placeholder="Search apps…" aria-label="Search apps" value={query} onChange={(e) => setQuery(e.target.value)} />
        </div>
        <Button variant={favoritesOnly ? "primary" : "outline"} aria-pressed={favoritesOnly} onClick={() => setFavoritesOnly((v) => !v)}>
          <StarIcon size={15} weight={favoritesOnly ? "fill" : "regular"} />
          My favorites
        </Button>
      </div>
      <div role="tablist" aria-label="Categories" className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1 [scrollbar-width:none]">
        {GALLERY_TABS.map((t) => (
          <button
            key={t.value}
            type="button"
            role="tab"
            aria-selected={tab === t.value}
            onClick={() => setTab(t.value)}
            className={cn(
              "tactile shrink-0 rounded-full border px-3 py-1 text-sm",
              tab === t.value
                ? "border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] text-[var(--color-accent)]"
                : "border-[var(--color-border)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      {list.error && !list.data ? (
        <LoadFailed message={errorText(list.error, "Could not load the apps")} onRetry={() => void list.refetch()} />
      ) : !list.data ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3" role="status" aria-label="Loading apps">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="aspect-[16/13]" />
          ))}
        </div>
      ) : apps.length === 0 ? (
        <EmptyState
          icon={<AppWindowIcon size={26} />}
          title={filtered ? "No apps match" : "No apps yet"}
          hint={
            filtered
              ? favoritesOnly && !query.trim() && tab === "all"
                ? "Star an app to find it here."
                : "Try another category or search, or clear the filters."
              : "Publish an app from Library → Apps, or from an app in a room, and it appears here."
          }
          action={
            filtered ? (
              <Button
                variant="outline"
                onClick={() => {
                  setTab("all");
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
        <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3" aria-label="Apps">
          {apps.map((c) => (
            <li key={c.id} className="min-w-0">
              <GalleryCard app={c} onOpen={() => setOpenId(c.id)} onStar={() => void toggleStar(c)} />
            </li>
          ))}
        </ul>
      )}
      <AppDetailDialog
        app={open}
        onClose={() => setOpenId(null)}
        onStar={() => open && void toggleStar(open)}
        onUsed={(roomId, artifactId) => {
          setOpenId(null);
          onOpenRoom(roomId, artifactId);
        }}
      />
    </section>
  );
}

function GalleryCard({ app, onOpen, onStar }: { readonly app: ContentComponent; readonly onOpen: () => void; readonly onStar: () => void }) {
  const [ref, visible] = useInView<HTMLButtonElement>();
  const label = contentLabel(app);
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
            seed={app.id}
            name={label}
            cover={componentCover(app.coverUrl)}
            enabled={visible}
            className="transition-transform duration-300 group-hover/app:scale-[1.03]"
          />
        </span>
        <span className="flex min-w-0 flex-col gap-0.5 px-3 pb-3 pr-16 pt-2.5">
          <span className="truncate font-medium text-[var(--color-fg)]" title={label}>
            {label}
          </span>
          <span className="truncate text-xs text-[var(--color-fg-subtle)]">
            <AppAuthorName app={app} />
          </span>
        </span>
      </button>
      <FavoriteStar app={app} onToggle={onStar} className="absolute bottom-2 right-2" />
    </Card>
  );
}
