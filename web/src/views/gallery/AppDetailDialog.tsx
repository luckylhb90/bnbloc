import { useEffect, useState } from "react";
import { BuildingsIcon, CopySimpleIcon, PlayIcon, StarIcon } from "@phosphor-icons/react";
import { appCategoryLabel } from "@/lib/appGallery";
import { contentLabel, type ContentComponent, type ContentVersionDetail } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { canShare } from "@/lib/contentSharing";
import { errorText } from "@/lib/errors";
import { exactTime, relativeTime } from "@/lib/format";
import { invalidateQueries } from "@/lib/useQuery";
import { Markdown } from "@/lib/markdown";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { Dialog } from "@/ui/Dialog";
import { PublishDialog } from "@/views/library/ContentSharing";
import { AppCover, componentCover } from "@/views/workbench/AppCover";
import { AppAuthorName, GALLERY_QUERY } from "./AppParts";
import { AppDemo } from "./AppDemo";
import { UseAppDialog } from "./UseAppDialog";

// AppDetailDialog is one app of the gallery (PRD §7, "Detail page"): its
// cover, its README (the version's README.md, rendered by the markdown
// viewer), author, version, update time, category and tags; and the star,
// Try demo (the app's site with an in-memory data space of the page:
// AppDemo) and Use this app (a copy in a room of the member's:
// UseAppDialog). The author of their own app may also publish it to the
// organization (the Library's publish flow).

const README = "README.md";

export function AppDetailDialog({
  app,
  onClose,
  onStar,
  onUsed,
}: {
  readonly app: ContentComponent | null;
  readonly onClose: () => void;
  readonly onStar: () => void;
  readonly onUsed: (roomId: string, artifactId: string) => void;
}) {
  const [version, setVersion] = useState<{ key: string; detail: ContentVersionDetail | null; error: unknown } | null>(null);
  const [demo, setDemo] = useState(false);
  const [using, setUsing] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const key = app ? `${app.id}@${app.version}` : "";

  useEffect(() => {
    setDemo(false);
    setUsing(false);
    if (!app) return;
    let live = true;
    contentApi
      .version(app.id, app.version)
      .then((detail) => live && setVersion({ key, detail, error: null }))
      .catch((error: unknown) => live && setVersion({ key, detail: null, error }));
    return () => {
      live = false;
    };
    // The version's identity is the key.
  }, [key]);

  if (!app) return null;
  const label = contentLabel(app);
  const loaded = version?.key === key ? version : null;
  const readme = loaded?.detail?.files.find((f) => f.path === README)?.text;
  const category = appCategoryLabel(app.category);
  return (
    <>
      <Dialog
        open={!demo && !using && !publishing}
        onClose={onClose}
        size="lg"
        title={label}
        description={app.description || undefined}
        footer={
          <>
            {canShare(app) && (
              <Button variant="ghost" className="mr-auto" onClick={() => setPublishing(true)}>
                <BuildingsIcon size={15} />
                Publish to organization…
              </Button>
            )}
            <Button variant="outline" aria-pressed={app.favorite} onClick={onStar}>
              <StarIcon size={15} weight={app.favorite ? "fill" : "regular"} />
              {app.favorite ? "Favorite" : "Add to favorites"}
            </Button>
            <Button variant="outline" onClick={() => setDemo(true)}>
              <PlayIcon size={15} />
              Try demo
            </Button>
            <Button variant="primary" onClick={() => setUsing(true)}>
              <CopySimpleIcon size={15} />
              Use this app
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4 text-sm">
          <div className="aspect-[16/10] w-full overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)]">
            <AppCover seed={app.id} name={label} cover={componentCover(app.coverUrl)} />
          </div>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5 text-xs text-[var(--color-fg-subtle)]">
            <span className="text-[var(--color-fg-muted)]">
              <AppAuthorName app={app} />
            </span>
            <span aria-hidden>·</span>
            <span className="tabular">v{app.version}</span>
            <span aria-hidden>·</span>
            <span title={exactTime(app.updatedAtMs)}>updated {relativeTime(app.updatedAtMs)}</span>
            {category && <Badge tone="accent">{category}</Badge>}
            {(app.tags ?? []).map((t) => (
              <Badge key={t}>{t}</Badge>
            ))}
          </div>
          {loaded?.error ? (
            <p className="text-[var(--color-fg-muted)]">{errorText(loaded.error, "Could not load the app's description")}</p>
          ) : !loaded ? (
            <p className="text-[var(--color-fg-subtle)]" role="status">
              Loading…
            </p>
          ) : readme?.trim() ? (
            <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] px-4 py-3">
              <Markdown text={readme} />
            </div>
          ) : (
            <p className="text-[var(--color-fg-subtle)]">This app has no README.</p>
          )}
        </div>
      </Dialog>
      <AppDemo app={demo ? app : null} onClose={() => setDemo(false)} onUse={() => {
          setDemo(false);
          setUsing(true);
        }} />
      <UseAppDialog app={using ? app : null} onClose={() => setUsing(false)} onUsed={onUsed} />
      <PublishDialog
        component={publishing ? app : null}
        onClose={() => setPublishing(false)}
        onChanged={() => invalidateQueries(GALLERY_QUERY)}
      />
    </>
  );
}
