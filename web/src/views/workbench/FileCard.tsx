import { useEffect, useRef, useState } from "react";
import { ArrowSquareOutIcon, DotsThreeIcon, ShareNetworkIcon, StorefrontIcon } from "@phosphor-icons/react";
import type { Artifact, ArtifactVersion } from "@/lib/api";
import { deliverableTypeLabel, fileCategory, isPreviewableImage, isSite, versionFacts } from "@/lib/artifactFiles";
import { cn } from "@/lib/cn";
import { formatBytes, relativeTime } from "@/lib/format";
import { Menu, MenuItem } from "@/ui/Menu";
import { ActorLabel } from "@/views/room/ActorLabel";
import { AppCover, siteCover } from "./AppCover";
import { PublishWorkDialog } from "@/views/gallery/PublishWorkDialog";
import { AppDataLine } from "./AppDataLine";
import { useDeliverableOwner } from "./deliverableOwner";
import { COVER_TEXT_MAX_BYTES, fileInfoFor, loadCardImage, useArtifactDetail, versionMeta, type CardImageSource } from "./artifactData";
import { selectViewer, useViewerRegistry, type ArtifactViewer } from "@/views/artifact/registry";
import { useCoverJob } from "@/lib/useCoverJob";
import { CoverJob } from "./CoverJob";
import { categoryIcons } from "./fileIcons";
import { ShareDialog } from "./ShareDialog";

// FileCard: one deliverable as a card — a thumbnail (the version's stored
// thumbnail, else the image itself for images, else an interactive app's
// cover — its own or the generated one —, else a type icon), name, type,
// size and version. A card is a pointer: clicking opens the artifact in a
// workbench tab; it never runs or renders the file itself. Its menu offers
// Open and Share (the share dialog: who may open a link, its version and
// lifetime), and for the room's deliverables' owners (useDeliverableOwner)
// Publish to gallery, whatever the deliverable. An interactive app's card
// (the server's isApp) adds its
// owners' data line (AppDataLine) below. Detail and images load only once
// the card scrolls into view.

/** Images above this are not fetched just to draw a card. */
const CARD_IMAGE_MAX_BYTES = 8 * 1024 * 1024;
/** Files above this are not rendered just to make a card's cover. */
const COVER_RENDER_MAX_BYTES = 8 * 1024 * 1024;

export function useInView<T extends Element>(): [React.RefObject<T | null>, boolean] {
  const ref = useRef<T | null>(null);
  const [seen, setSeen] = useState(false);
  useEffect(() => {
    const el = ref.current;
    if (!el || seen) return;
    if (typeof IntersectionObserver === "undefined") {
      setSeen(true);
      return;
    }
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) setSeen(true);
      },
      { rootMargin: "200px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [seen]);
  return [ref, seen];
}

function useCardImage(roomId: string, artifact: Artifact | null, meta: ArtifactVersion | undefined, viewer: ArtifactViewer | null): string | null {
  const [url, setUrl] = useState<string | null>(null);
  // The viewer that would open the file says how its card gets a picture.
  const source: CardImageSource | null = !artifact || !meta
    ? null
    : meta.thumbnail
      ? "thumbnail"
      : isPreviewableImage(versionFacts(artifact, meta)) && (meta.sizeBytes ?? 0) <= CARD_IMAGE_MAX_BYTES && meta.storage === "blob"
        ? "content"
        : viewer?.cover === "text" && (meta.sizeBytes ?? 0) <= COVER_TEXT_MAX_BYTES
          ? "excerpt"
          : null;
  // The image is identified by artifact, version and source; the latest
  // objects are read through a ref so a detail refetch does not reload it.
  const latest = useRef({ artifact, meta });
  latest.current = { artifact, meta };
  const key = source && artifact && meta ? `${artifact.artifactId}:${meta.version}:${source}` : "";
  useEffect(() => {
    setUrl(null);
    const { artifact: a, meta: m } = latest.current;
    if (!key || !a || !m || !source) return;
    let cancelled = false;
    loadCardImage(roomId, a, m, source)
      .then((u) => {
        if (!cancelled) setUrl(u);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [roomId, key, source]);
  return url;
}

export function FileCard({
  roomId,
  artifactId,
  onOpen,
  className,
}: {
  readonly roomId: string;
  readonly artifactId: string;
  readonly onOpen: (artifactId: string) => void;
  readonly className?: string;
}) {
  const [ref, visible] = useInView<HTMLButtonElement>();
  const { detail, error } = useArtifactDetail(roomId, artifactId, visible);
  const artifact = detail?.artifact ?? null;
  const meta = artifact ? versionMeta(detail, artifact.currentVersion) : undefined;
  useViewerRegistry();
  const viewer = artifact && meta ? selectViewer(fileInfoFor(artifact, artifact.currentVersion, meta)) : null;
  const image = useCardImage(roomId, artifact, meta, viewer);
  // A card in view whose version has no cover, and whose viewer sends one when
  // it renders, has the file rendered once out of sight (lib/coverJobs).
  const wantsRender =
    visible && !!artifact && !!meta && !meta.thumbnail && !artifact.isApp && viewer?.cover === "render" && (meta.sizeBytes ?? 0) <= COVER_RENDER_MAX_BYTES;
  const rendering = useCoverJob(`${roomId}/${artifactId}/${meta?.version ?? 0}`, wantsRender);
  const [tiny, setTiny] = useState(false);
  const [sharingOpen, setSharingOpen] = useState(false);
  const [publishing, setPublishing] = useState(false);
  // The room's deliverables' owners publish any of them to the gallery.
  const owner = useDeliverableOwner();
  if (error) return null; // deleted or inaccessible: no card
  const facts = artifact ? versionFacts(artifact, meta) : null;
  const file = artifact ? fileInfoFor(artifact, artifact.currentVersion, meta) : null;
  const Icon = categoryIcons[facts ? fileCategory(facts) : "file"];
  const site = !!facts && isSite(facts);
  // An interactive app is the server's answer (isApp), never re-derived.
  const app = !!artifact?.isApp;
  return (
    <div className={cn("flex min-w-0 flex-col", className)}>
      {rendering && artifact && meta && viewer && <CoverJob roomId={roomId} artifact={artifact} meta={meta} viewer={viewer} />}
      <div className="group/card relative flex min-w-0">
        <button
          ref={ref}
          type="button"
          onClick={() => onOpen(artifactId)}
          title={file ? `Open ${file.name}` : "Open"}
          aria-label={file ? `Open ${file.name}` : "Open deliverable"}
          className="tactile group flex min-w-0 flex-1 flex-col overflow-hidden rounded-[var(--radius-lg)] border border-[var(--color-border)] bg-[var(--color-surface-2)] text-left hover:border-[var(--color-border-strong)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
        >
          <span className="relative grid aspect-[16/10] w-full place-items-center overflow-hidden bg-[var(--color-surface)]">
            {image ? (
              <img
                src={image}
                alt=""
                loading="lazy"
                decoding="async"
                onLoad={(e) => setTiny(e.currentTarget.naturalWidth > 0 && e.currentTarget.naturalWidth < 160 && e.currentTarget.naturalHeight < 160)}
                className={cn(
                  "h-full w-full transition-transform duration-300 group-hover:scale-[1.03]",
                  // Pixel art / icons: whole and crisp instead of a blurred crop.
                  tiny ? "object-contain p-2 [image-rendering:pixelated]" : "object-cover",
                )}
              />
            ) : artifact?.isApp && file ? (
              <AppCover seed={artifactId} name={file.name} cover={siteCover(roomId, artifactId, artifact.currentVersion)} enabled={visible} />
            ) : (
              <Icon size={26} className="text-[var(--color-fg-subtle)]" aria-hidden />
            )}
            {artifact && artifact.currentVersion > 1 && (
              <span className="absolute right-1.5 top-1.5 rounded-full bg-[var(--color-bg)]/80 px-1.5 text-[10px] font-medium tabular text-[var(--color-fg-muted)]">
                v{artifact.currentVersion}
              </span>
            )}
          </span>
          <span className="flex min-w-0 flex-col gap-0.5 px-2 py-1.5">
            <span className="truncate text-xs font-medium text-[var(--color-fg)]">{file?.name ?? "…"}</span>
            <span className="truncate text-[11px] text-[var(--color-fg-subtle)]">
              {facts
                ? [deliverableTypeLabel(!!artifact?.isApp, facts), meta?.sizeBytes !== undefined ? formatBytes(meta.sizeBytes) : ""].filter(Boolean).join(" · ")
                : " "}
            </span>
          </span>
        </button>
        {artifact && file && (
          <>
            <Menu
              label={`${file.name} actions`}
              className="absolute left-1.5 top-1.5"
              menuClassName="absolute left-0 top-full mt-1 w-48"
              trigger={(props, open) => (
                <button
                  {...props}
                  title="More actions"
                  aria-label={`More actions for ${file.name}`}
                  className={cn(
                    "tap tactile grid size-6 place-items-center rounded-full bg-[var(--color-bg)]/85 text-[var(--color-fg-muted)] shadow-[var(--shadow-pop)] hover:text-[var(--color-fg)] focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60 [@media(hover:hover)]:opacity-0 [@media(hover:hover)]:group-hover/card:opacity-100",
                    open && "opacity-100 [@media(hover:hover)]:opacity-100",
                  )}
                >
                  <DotsThreeIcon size={16} weight="bold" />
                </button>
              )}
            >
              <MenuItem icon={ArrowSquareOutIcon} onSelect={() => onOpen(artifactId)}>
                Open
              </MenuItem>
              <MenuItem icon={ShareNetworkIcon} onSelect={() => setSharingOpen(true)}>
                Share…
              </MenuItem>
              {owner && (
                <MenuItem icon={StorefrontIcon} onSelect={() => setPublishing(true)}>
                  Publish to gallery…
                </MenuItem>
              )}
            </Menu>
            <PublishWorkDialog deliverable={publishing ? { roomId, artifactId, title: file.name } : null} onClose={() => setPublishing(false)} />
            <ShareDialog
              open={sharingOpen}
              onClose={() => setSharingOpen(false)}
              roomId={roomId}
              artifactId={artifactId}
              fileName={file.name}
              version={artifact.currentVersion}
              kind={app ? "app" : site ? "site" : "file"}
            />
          </>
        )}
      </div>
      {app && file && <AppDataLine compact roomId={roomId} artifactId={artifactId} appName={file.name} />}
    </div>
  );
}

/** A list row of the deliverables view (metadata from the list response; no
 *  per-row fetch). */
export function FileRow({ artifact, onOpen }: { readonly artifact: Artifact; readonly onOpen: (artifactId: string) => void }) {
  const facts = versionFacts(artifact);
  const Icon = categoryIcons[fileCategory(facts)];
  const file = fileInfoFor(artifact, artifact.currentVersion);
  return (
    <button
      type="button"
      onClick={() => onOpen(artifact.artifactId)}
      className="tactile flex w-full min-w-0 items-center gap-2.5 rounded-[var(--radius)] px-2 py-2 text-left text-sm hover:bg-[var(--color-surface-2)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60"
    >
      <Icon size={18} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[var(--color-fg)]">{file.name}</span>
        <span className="flex items-center gap-1 text-[11px] text-[var(--color-fg-subtle)]">
          <span>{deliverableTypeLabel(artifact.isApp, facts)}</span>
          <span>·</span>
          <ActorLabel actor={artifact.createdBy} />
        </span>
      </span>
      <span className="shrink-0 text-right text-[11px] text-[var(--color-fg-subtle)] tabular">
        v{artifact.currentVersion}
        <span className="block">{relativeTime(artifact.updatedAtMs)}</span>
      </span>
    </button>
  );
}
