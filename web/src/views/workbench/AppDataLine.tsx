import { useState } from "react";
import { ArrowClockwiseIcon, DatabaseIcon, DotsThreeIcon, DownloadSimpleIcon, FileCsvIcon, TrashIcon } from "@phosphor-icons/react";
import { appCollectionCounts, appDataLineShown, appExportFileName, appStatsAnswer, collectionsLabel, documentsLabel } from "@/lib/appData";
import { appDataApi } from "@/lib/appDataApi";
import { useAuth } from "@/lib/auth";
import { cn } from "@/lib/cn";
import { errorText } from "@/lib/errors";
import { can } from "@/lib/permissions";
import { saveBlob } from "@/lib/saveBlob";
import { invalidateQueries, queryKey, useQuery } from "@/lib/useQuery";
import { AppExportCSV, AppExportJSON, EventAppChanged, type AppChanged } from "@/lib/wire.gen";
import { ConfirmDialog } from "@/ui/Dialog";
import { Menu, MenuItem } from "@/ui/Menu";
import { useToast } from "@/ui/Toast";

// AppDataLine is an interactive app's data line, for its owners only, on
// the site's deliverable card (compact) and in the artifact viewer: the
// document count per collection, Export (JSON of every collection, CSV of
// one) and Clear data. It shows only when the app has data; the owner rule
// and the visibility rule are lib/appData.ts (the owners' stats route
// decides who is an owner). The counts follow the room's app_changed events
// (applyAppChanged, from the room view's and the pop-out's streams).

const STATS = "app-stats";

function statsName(roomId: string, artifactId: string): string {
  return `${STATS}:${roomId}:${artifactId}`;
}

// The room's app_changed events refresh a counted app at most this often.
const CHANGE_COALESCE_MS = 2000;
const pendingChanges = new Map<string, ReturnType<typeof setTimeout>>();

/** applyAppChanged takes a room event: an app_changed refreshes the app's
 *  counts (coalesced: a busy app's writes refetch once per window). It
 *  reports whether the event was one. */
export function applyAppChanged(roomId: string, e: { readonly kind?: string; readonly payload?: unknown }): boolean {
  if (e.kind !== EventAppChanged) return false;
  const appId = (e.payload as Partial<AppChanged> | null | undefined)?.appId;
  if (typeof appId === "string" && appId) appDataChanged(roomId, appId);
  return true;
}

function appDataChanged(roomId: string, artifactId: string): void {
  const name = statsName(roomId, artifactId);
  if (pendingChanges.has(name)) return;
  pendingChanges.set(
    name,
    setTimeout(() => {
      pendingChanges.delete(name);
      invalidateQueries(name);
    }, CHANGE_COALESCE_MS),
  );
}

const CHIP =
  "tactile inline-flex h-7 items-center gap-1 rounded-[var(--radius)] px-2 text-xs text-[var(--color-fg-muted)] hover:bg-[var(--color-surface-2)] hover:text-[var(--color-fg)] disabled:opacity-40";

export function AppDataLine({
  roomId,
  artifactId,
  appName,
  compact = false,
}: {
  readonly roomId: string;
  readonly artifactId: string;
  // The app's name, for export file names.
  readonly appName: string;
  // The card's one-line form (actions in a menu).
  readonly compact?: boolean;
}) {
  const { subject, tenant, role } = useAuth();
  const toast = useToast();
  const stats = useQuery(queryKey(statsName(roomId, artifactId), { subject, tenant }), () => appDataApi.stats(roomId, artifactId), {
    staleMs: 15_000,
  });
  const answer = appStatsAnswer(stats);
  const [confirmClear, setConfirmClear] = useState(false);
  const [busy, setBusy] = useState(false);

  if (answer.kind === "failed") {
    return (
      <div className={cn("flex min-w-0 items-center gap-1.5 text-[11px] text-[var(--color-fg-subtle)]", compact ? "px-1 pt-1" : "border-b border-[var(--color-border)] px-3 py-1")}>
        <span className="min-w-0 truncate" title={errorText(answer.error, "Could not load this app's data")}>
          App data did not load
        </span>
        <button type="button" onClick={() => void stats.refetch()} className={CHIP} title="Retry" aria-label="Retry loading the app's data">
          <ArrowClockwiseIcon size={12} />
          {!compact && "Retry"}
        </button>
      </div>
    );
  }
  if (!appDataLineShown(answer)) return null;
  const collections = appCollectionCounts(answer.stats);
  const total = documentsLabel(answer.stats.documents);
  const perCollection = collectionsLabel(answer.stats);
  const mayClear = can(role, "drive_agent");

  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      toast.error(errorText(e, what));
    } finally {
      setBusy(false);
    }
  };
  // The server streams the export and audits it (owners only).
  const exportJson = () =>
    run("Export failed", async () => {
      saveBlob(await appDataApi.export(roomId, artifactId, AppExportJSON), appExportFileName(appName, "json"));
    });
  const exportCsv = (collection: string) =>
    run("Export failed", async () => {
      saveBlob(await appDataApi.export(roomId, artifactId, AppExportCSV, collection), appExportFileName(appName, "csv", collection));
    });
  const clear = () =>
    run("Could not clear the app's data", async () => {
      const wiped = await appDataApi.wipe(roomId, artifactId);
      toast.success(`Cleared ${documentsLabel(wiped.deleted)}`);
      invalidateQueries(statsName(roomId, artifactId));
    });

  const exportItems = (
    <>
      <MenuItem icon={DownloadSimpleIcon} onSelect={() => void exportJson()}>
        JSON (all collections)
      </MenuItem>
      {collections.map((c) => (
        <MenuItem key={c.name} icon={FileCsvIcon} onSelect={() => void exportCsv(c.name)}>
          CSV · {c.name}
        </MenuItem>
      ))}
    </>
  );
  const dialog = (
    <ConfirmDialog
      open={confirmClear}
      onClose={() => setConfirmClear(false)}
      onConfirm={clear}
      title="Clear this app's data?"
      description={`This permanently deletes the app's ${total} for everyone, including what people entered through share links. The app and its links stay. It cannot be undone: export the data first to keep a copy.`}
      confirmLabel="Clear data"
      danger
    />
  );

  if (compact) {
    return (
      <div className="flex min-w-0 items-center gap-1 px-1 pt-1 text-[11px] text-[var(--color-fg-subtle)]" data-app-data>
        <DatabaseIcon size={12} className="shrink-0" aria-hidden />
        <span className="min-w-0 flex-1 truncate tabular" title={`${total}: ${perCollection}`}>
          {perCollection}
        </span>
        <Menu
          label="App data actions"
          className="relative shrink-0"
          menuClassName="absolute right-0 bottom-full mb-1 w-52"
          trigger={(props, open) => (
            <button
              {...props}
              disabled={busy}
              title="App data"
              aria-label={`App data actions for ${appName}`}
              className={cn("tap tactile grid size-5 place-items-center rounded-full hover:text-[var(--color-fg)]", open && "text-[var(--color-fg)]")}
            >
              <DotsThreeIcon size={14} weight="bold" />
            </button>
          )}
        >
          {exportItems}
          {mayClear && (
            <MenuItem icon={TrashIcon} onSelect={() => setConfirmClear(true)}>
              Clear data…
            </MenuItem>
          )}
        </Menu>
        {dialog}
      </div>
    );
  }

  return (
    <div className="flex min-w-0 shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-[var(--color-border)] px-3 py-1 text-xs text-[var(--color-fg-muted)]" data-app-data>
      <DatabaseIcon size={14} className="shrink-0 text-[var(--color-fg-subtle)]" aria-hidden />
      <span className="shrink-0 font-medium text-[var(--color-fg)] tabular">{total}</span>
      <span className="min-w-0 flex-1 truncate tabular" title={perCollection}>
        {perCollection}
      </span>
      <Menu
        label="Export app data"
        className="relative shrink-0"
        menuClassName="absolute right-0 top-full mt-1 w-52"
        trigger={(props) => (
          <button {...props} disabled={busy} className={CHIP}>
            <DownloadSimpleIcon size={14} /> Export
          </button>
        )}
      >
        {exportItems}
      </Menu>
      {mayClear && (
        <button type="button" disabled={busy} onClick={() => setConfirmClear(true)} className={CHIP}>
          <TrashIcon size={14} /> Clear data
        </button>
      )}
      {dialog}
    </div>
  );
}
