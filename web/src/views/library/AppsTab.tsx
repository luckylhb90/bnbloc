import { useCallback, useEffect, useState } from "react";
import { AppWindowIcon, ArrowSquareOutIcon, StorefrontIcon, DatabaseIcon, DotsThreeIcon, DownloadSimpleIcon, LinkIcon, ShareNetworkIcon, SignInIcon } from "@phosphor-icons/react";
import { appDataApi } from "@/lib/appDataApi";
import { appExportFileName, documentsLabel } from "@/lib/appData";
import { cn } from "@/lib/cn";
import { errorText } from "@/lib/errors";
import { exactTime, relativeTime } from "@/lib/format";
import { routeHash } from "@/lib/hashRoute";
import { appLinkBadge, type MyApp } from "@/lib/myApps";
import { myAppsApi } from "@/lib/myAppsApi";
import { saveBlob } from "@/lib/saveBlob";
import { useTenantPolicy } from "@/lib/tenantPolicy";
import { AppExportJSON } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Badge, Card, Skeleton } from "@/ui/Card";
import { Menu, MenuItem } from "@/ui/Menu";
import { EmptyState, PageHeader } from "@/ui/Page";
import { LoadFailed } from "@/ui/QueryState";
import { useToast } from "@/ui/Toast";
import { AgentHandle, UserName } from "@/views/UserName";
import { AppCover, siteCover } from "@/views/workbench/AppCover";
import { useInView } from "@/views/workbench/FileCard";
import { ShareDialog } from "@/views/workbench/ShareDialog";
import { PublishAppDialog } from "@/views/gallery/PublishAppDialog";

// AppsTab is the Library's "My apps" (docs/prd/2026-10-13-app-gallery.md
// §5): the interactive apps of every room of the organization where the
// member is one of the apps' owners (GET /v1/my/apps, the server's answer),
// newest first. Each card: the cover (the site's own, else the generated
// one), the name, and its room, producer, link status and document count;
// open (the app in its room), share or manage links (the share dialog),
// export the data (the server's audited export), publish it to the app
// gallery (PublishAppDialog) and go to the room.

const PAGE = 24;

export function AppsTab({ tabs, onOpenRoom }: { readonly tabs: React.ReactNode; readonly onOpenRoom: (roomId: string) => void }) {
  const [apps, setApps] = useState<MyApp[] | null>(null);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [epoch, setEpoch] = useState(0);
  const toast = useToast();
  const reload = useCallback(() => setEpoch((n) => n + 1), []);

  useEffect(() => {
    let live = true;
    setLoadError(null);
    myAppsApi
      .list(undefined, PAGE)
      .then((r) => {
        if (!live) return;
        setApps(r.apps ?? []);
        setCursor(r.nextCursor || undefined);
      })
      .catch((e) => live && setLoadError(errorText(e, "Could not load your apps")));
    return () => {
      live = false;
    };
  }, [epoch]);

  const loadMore = async () => {
    if (!cursor) return;
    setLoadingMore(true);
    try {
      const r = await myAppsApi.list(cursor, PAGE);
      setApps((cur) => {
        const seen = new Set((cur ?? []).map((a) => a.appId));
        return [...(cur ?? []), ...(r.apps ?? []).filter((a) => !seen.has(a.appId))];
      });
      setCursor(r.nextCursor || undefined);
    } catch (e) {
      toast.error(errorText(e, "Could not load more apps"));
    } finally {
      setLoadingMore(false);
    }
  };

  return (
    <>
      <PageHeader title="Library" subtitle="Interactive apps in rooms you can manage" actions={tabs} />
      <div className="p-4 sm:p-6">
        {loadError ? (
          <LoadFailed message={loadError} onRetry={reload} />
        ) : apps === null ? (
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4" role="status" aria-label="Loading your apps">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="aspect-[16/13]" />
            ))}
          </div>
        ) : apps.length === 0 ? (
          <EmptyState
            icon={<AppWindowIcon size={26} />}
            title="No apps yet"
            hint="Ask an agent in a room to build one — a poll, a sign-up sheet or a survey. Apps in rooms where you can chat appear here."
          />
        ) : (
          <>
            <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4" aria-label="Your apps">
              {apps.map((a) => (
                <li key={a.appId} className="min-w-0">
                  <AppCard app={a} onOpenRoom={onOpenRoom} onLinksChanged={reload} />
                </li>
              ))}
            </ul>
            {cursor && (
              <div className="mt-4 flex justify-center">
                <Button variant="ghost" disabled={loadingMore} onClick={() => void loadMore()}>
                  {loadingMore ? "Loading…" : "Load more"}
                </Button>
              </div>
            )}
          </>
        )}
      </div>
    </>
  );
}

function AppCard({ app, onOpenRoom, onLinksChanged }: { readonly app: MyApp; readonly onOpenRoom: (roomId: string) => void; readonly onLinksChanged: () => void }) {
  const toast = useToast();
  const org = useTenantPolicy()?.name ?? "";
  const [ref, visible] = useInView<HTMLButtonElement>();
  const [sharing, setSharing] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const badge = appLinkBadge(app.links ?? [], org, Date.now());
  const open = () => {
    window.location.hash = routeHash({ kind: "room", roomId: app.roomId, artifactId: app.appId });
  };
  const exportData = async () => {
    if (exporting) return;
    setExporting(true);
    try {
      saveBlob(await appDataApi.export(app.roomId, app.appId, AppExportJSON), appExportFileName(app.name, "json"));
    } catch (e) {
      toast.error(errorText(e, "Export failed"));
    } finally {
      setExporting(false);
    }
  };
  return (
    <Card className="group/app relative flex h-full min-w-0 flex-col overflow-hidden">
      <button
        ref={ref}
        type="button"
        onClick={open}
        aria-label={`Open ${app.name} in ${app.roomName || "its room"}`}
        className="tactile flex min-w-0 flex-1 flex-col text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--color-accent)]/60"
      >
        <span className="relative block aspect-[16/10] w-full overflow-hidden border-b border-[var(--color-border)] bg-[var(--color-surface-2)]">
          <AppCover seed={app.appId} name={app.name} cover={siteCover(app.roomId, app.appId, app.version)} enabled={visible} />
        </span>
        <span className="flex min-w-0 flex-col gap-1 px-3 pb-3 pt-2.5">
          <span className="truncate font-medium text-[var(--color-fg)]" title={app.name}>
            {app.name}
          </span>
          <span className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-[var(--color-fg-subtle)]">
            <span className="min-w-0 max-w-full truncate" title={app.roomName}>
              {app.roomName || "Untitled room"}
            </span>
            <span aria-hidden>·</span>
            <Producer app={app} />
            <span aria-hidden>·</span>
            <span title={exactTime(app.updatedAtMs)}>v{app.version}, {relativeTime(app.updatedAtMs)}</span>
          </span>
          <span className="flex min-w-0 flex-wrap items-center gap-1.5 pt-0.5">
            <Badge tone={badge.tone === "anyone" ? "warning" : badge.tone === "none" ? "neutral" : "accent"} title={badge.title}>
              <LinkIcon size={11} aria-hidden />
              {badge.label}
            </Badge>
            <Badge title={app.documents > 0 ? "Documents in the app's data" : "The app holds no data yet"}>
              <DatabaseIcon size={11} aria-hidden />
              {documentsLabel(app.documents)}
            </Badge>
          </span>
        </span>
      </button>
      <Menu
        label={`${app.name} actions`}
        className="absolute right-2 top-2"
        menuClassName="absolute right-0 top-full mt-1 w-48"
        trigger={(props, isOpen) => (
          <button
            {...props}
            title="More actions"
            aria-label={`More actions for ${app.name}`}
            className={cn(
              "tap tactile grid size-8 place-items-center rounded-full bg-[var(--color-bg)]/85 text-[var(--color-fg-muted)] shadow-[var(--shadow-pop)] hover:text-[var(--color-fg)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]/60",
              isOpen && "text-[var(--color-fg)]",
            )}
          >
            <DotsThreeIcon size={18} weight="bold" />
          </button>
        )}
      >
        <MenuItem icon={ArrowSquareOutIcon} onSelect={open}>
          Open
        </MenuItem>
        <MenuItem icon={ShareNetworkIcon} onSelect={() => setSharing(true)}>
          {app.links.length > 0 ? "Manage links…" : "Share…"}
        </MenuItem>
        {app.documents > 0 && (
          <MenuItem icon={DownloadSimpleIcon} onSelect={() => void exportData()}>
            {exporting ? "Exporting…" : "Export data (JSON)"}
          </MenuItem>
        )}
        <MenuItem icon={StorefrontIcon} onSelect={() => setPublishing(true)}>
          Publish to gallery…
        </MenuItem>
        <MenuItem icon={SignInIcon} onSelect={() => onOpenRoom(app.roomId)}>
          Go to room
        </MenuItem>
      </Menu>
      <PublishAppDialog site={publishing ? { roomId: app.roomId, artifactId: app.appId, title: app.name } : null} onClose={() => setPublishing(false)} />
      <ShareDialog
        open={sharing}
        onClose={() => {
          setSharing(false);
          onLinksChanged();
        }}
        roomId={app.roomId}
        artifactId={app.appId}
        fileName={app.name}
        version={app.version}
        kind="app"
      />
    </Card>
  );
}

function Producer({ app }: { readonly app: MyApp }) {
  const p = app.producer;
  if (p.provider && p.owner) return <AgentHandle owner={p.owner} provider={p.provider} className="min-w-0 truncate" />;
  if (p.person) return <UserName id={p.person} className="min-w-0 truncate" />;
  return <span>an agent</span>;
}
