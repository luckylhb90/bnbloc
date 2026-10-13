import { StarIcon } from "@phosphor-icons/react";
import { appAuthor } from "@/lib/appGallery";
import { cn } from "@/lib/cn";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { UserName } from "@/views/UserName";

// The gallery's small parts, shared by its cards and an app's detail.

/** The gallery listing's query name (refetched after a publish). */
export const GALLERY_QUERY = "content-apps";

/** AppAuthorName is an app's author line: the person, or "Platform". */
export function AppAuthorName({ app }: { readonly app: ContentComponent }) {
  const author = appAuthor(app);
  if (author.kind === "platform") return <>Platform</>;
  return <UserName id={author.id} />;
}

/** FavoriteStar is the member's star on an app and how many of their
 *  organization starred it. */
export function FavoriteStar({ app, onToggle, className }: { readonly app: ContentComponent; readonly onToggle: () => void; readonly className?: string }) {
  const label = contentLabel(app);
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-pressed={app.favorite}
      aria-label={app.favorite ? `Remove ${label} from your favorites` : `Add ${label} to your favorites`}
      title={app.favorite ? "In your favorites" : "Add to your favorites"}
      className={cn(
        "tap tactile inline-flex h-8 items-center gap-1 rounded-full px-2 text-xs tabular hover:bg-[var(--color-surface-2)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60",
        app.favorite ? "text-[var(--color-warning)]" : "text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]",
        className,
      )}
    >
      <StarIcon size={16} weight={app.favorite ? "fill" : "regular"} />
      {app.favoriteCount > 0 && <span>{app.favoriteCount}</span>}
    </button>
  );
}
