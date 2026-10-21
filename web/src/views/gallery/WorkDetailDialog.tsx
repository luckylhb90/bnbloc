import { useEffect, useState } from "react";
import { BuildingsIcon, CopySimpleIcon, MagicWandIcon, PlayIcon, StarIcon } from "@phosphor-icons/react";
import { contentLabel, type ContentComponent, type ContentVersionDetail } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { canShare } from "@/lib/contentSharing";
import { errorText } from "@/lib/errors";
import { exactTime, relativeTime } from "@/lib/format";
import { Markdown } from "@/lib/markdown";
import { workCategoryLabel } from "@/lib/workGallery";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { Dialog } from "@/ui/Dialog";
import { PublishDialog } from "@/views/library/ContentSharing";
import { AppDemo } from "./AppDemo";
import { MakeSimilarDialog } from "./MakeSimilarDialog";
import { RecipePanel } from "./RecipePanel";
import { UseAppDialog } from "./UseAppDialog";
import { WorkPreview } from "./WorkPreview";
import { WorkAuthorName, WorkTypeBadge } from "./WorkParts";

// WorkDetailDialog is one work of the gallery (app gallery PRD §7, works
// PRD §2–§4): the work itself in the artifact viewers (WorkPreview), its
// README (the version's README.md, rendered by the markdown viewer),
// author, type, version, update time, category and tags, its recipe
// (RecipePanel); the star, Make similar (a draft of the recipe in a room
// of the member's: MakeSimilarDialog), and for an app Try demo (its site
// with an in-memory data space of the page: AppDemo) and Use this app (a
// copy in a room of the member's: UseAppDialog). The author of their own
// work may also publish it to the organization (the Library's publish
// flow).

const README = "README.md";

export function WorkDetailDialog({
  work,
  onClose,
  onStar,
  onUsed,
  onDrafted,
}: {
  readonly work: ContentComponent | null;
  readonly onClose: () => void;
  readonly onStar: () => void;
  readonly onUsed: (roomId: string, artifactId: string) => void;
  readonly onDrafted: (roomId: string) => void;
}) {
  const [version, setVersion] = useState<{ key: string; detail: ContentVersionDetail | null; error: unknown } | null>(null);
  const [demo, setDemo] = useState(false);
  const [using, setUsing] = useState(false);
  const [similar, setSimilar] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const key = work ? `${work.id}@${work.version}` : "";

  useEffect(() => {
    setDemo(false);
    setUsing(false);
    setSimilar(false);
    if (!work) return;
    let live = true;
    contentApi
      .version(work.id, work.version)
      .then((detail) => live && setVersion({ key, detail, error: null }))
      .catch((error: unknown) => live && setVersion({ key, detail: null, error }));
    return () => {
      live = false;
    };
    // The version's identity is the key.
  }, [key]);

  if (!work) return null;
  const label = contentLabel(work);
  const app = work.workType === "app";
  const loaded = version?.key === key ? version : null;
  const readme = loaded?.detail?.files.find((f) => f.path === README)?.text;
  const category = workCategoryLabel(work.category);
  return (
    <>
      <Dialog
        open={!demo && !using && !similar && !publishing}
        onClose={onClose}
        size="lg"
        title={label}
        description={work.description || undefined}
        footer={
          <>
            {canShare(work) && (
              <Button variant="ghost" className="mr-auto" onClick={() => setPublishing(true)}>
                <BuildingsIcon size={15} />
                Publish to organization…
              </Button>
            )}
            <Button variant="outline" aria-pressed={work.favorite} onClick={onStar}>
              <StarIcon size={15} weight={work.favorite ? "fill" : "regular"} />
              {work.favorite ? "Favorite" : "Add to favorites"}
            </Button>
            {app && (
              <Button variant="outline" onClick={() => setDemo(true)}>
                <PlayIcon size={15} />
                Try demo
              </Button>
            )}
            {app && (
              <Button variant="outline" onClick={() => setUsing(true)}>
                <CopySimpleIcon size={15} />
                Use this app
              </Button>
            )}
            <Button variant="primary" onClick={() => setSimilar(true)}>
              <MagicWandIcon size={15} />
              Make similar
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4 text-sm">
          <WorkPreview work={work} />
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5 text-xs text-[var(--color-fg-subtle)]">
            <WorkTypeBadge work={work} />
            <span className="text-[var(--color-fg-muted)]">
              <WorkAuthorName work={work} />
            </span>
            <span aria-hidden>·</span>
            <span className="tabular">v{work.version}</span>
            <span aria-hidden>·</span>
            <span title={exactTime(work.updatedAtMs)}>updated {relativeTime(work.updatedAtMs)}</span>
            {category && <Badge tone="accent">{category}</Badge>}
            {(work.tags ?? []).map((t) => (
              <Badge key={t}>{t}</Badge>
            ))}
          </div>
          <RecipePanel work={work} recipe={work.recipe} />
          {loaded?.error ? (
            <p className="text-[var(--color-fg-muted)]">{errorText(loaded.error, "Could not load the work's description")}</p>
          ) : !loaded ? (
            <p className="text-[var(--color-fg-subtle)]" role="status">
              Loading…
            </p>
          ) : readme?.trim() ? (
            <div className="rounded-[var(--radius-lg)] border border-[var(--color-border)] px-4 py-3">
              <Markdown text={readme} />
            </div>
          ) : null}
        </div>
      </Dialog>
      <AppDemo
        app={demo ? work : null}
        onClose={() => setDemo(false)}
        onUse={() => {
          setDemo(false);
          setUsing(true);
        }}
      />
      <UseAppDialog app={using ? work : null} onClose={() => setUsing(false)} onUsed={onUsed} />
      <MakeSimilarDialog work={similar ? work : null} onClose={() => setSimilar(false)} onDrafted={onDrafted} />
      <PublishDialog component={publishing ? work : null} onClose={() => setPublishing(false)} />
    </>
  );
}
