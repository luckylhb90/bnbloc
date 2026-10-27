import { req } from "@/lib/api";
import { inDesktopShell } from "@/lib/config";
import { previewerViewerId, type PreviewerDescriptor } from "@/lib/previewerFrame";
import { createPreviewerSync, settledWithin, type PreviewerSource } from "@/lib/previewerSync";
import { publicShareApi } from "@/lib/sharesApi";
import { registerArtifactViewer, unregisterArtifactViewer } from "./registry";
import { registerComparer, unregisterComparer } from "./compare/registry";
import { visiblePoll } from "@/lib/useVisiblePoll";

// Installed previewer components in the viewer registry. Every installed
// previewer becomes a "component" viewer whose host is RemotePreviewer; a
// previewer that leaves its source (uninstall, rollout off, opt-out, a sharer
// who no longer offers it) is unregistered on the next sync. The source is the
// member catalog (GET /v1/components?kind=previewer — rollout + tenant gated
// server-side) in the signed-in app, and a share's own list on the public share
// page (startSharePreviewers); lib/previewerSync keeps one active at a time.

const SYNC_INTERVAL_MS = 10 * 60 * 1000;
/** How long the share page waits for a share's previewers before it renders the
 *  file as it would without them. */
const SHARE_LOOKUP_MS = 5000;

const installed = new Map<string, PreviewerDescriptor>();

const memberCatalog: PreviewerSource = { read: () => req<unknown>("/v1/components?kind=previewer") };
const catalog = createPreviewerSync(memberCatalog, applyCatalog, (name) => installed.get(name));

/** The current descriptor of an installed previewer. */
export function installedPreviewer(name: string): PreviewerDescriptor | undefined {
  return installed.get(name);
}

/** Re-reads the catalog and reconciles the registry. Concurrent callers
 *  share one request; a failure keeps the previous registrations. */
export function syncInstalledPreviewers(): Promise<void> {
  return catalog.sync();
}

function applyCatalog(descriptors: readonly PreviewerDescriptor[]): void {
  const next = new Map(descriptors.map((d) => [d.name, d]));
  for (const name of installed.keys()) {
    if (!next.has(name)) remove(name);
  }
  for (const d of descriptors) {
    const prev = installed.get(d.name);
    installed.set(d.name, d);
    // A ticket refresh alone keeps the registration (and mounted frames).
    if (prev && sameRegistration(prev, d)) continue;
    register(d);
  }
}

// What an installed previewer contributes: its viewer, and its comparer
// when it reads its format into units (capability "units").
function register(d: PreviewerDescriptor): void {
  const match = { mimeTypes: d.mimeTypes, extensions: d.extensions };
  registerArtifactViewer({
    id: previewerViewerId(d.name),
    label: d.description ? `${d.name} — ${d.description}` : d.name,
    match,
    rank: d.rank,
    input: "blob",
    safety: "sandboxed",
    source: "component",
    // A previewer that declares the capability sends a cover when it renders.
    ...(d.capabilities.includes("thumbnail") ? { cover: "render" as const } : {}),
    load: () => import("./RemotePreviewer").then((m) => ({ default: m.previewerHost(d.name) })),
  });
  if (d.capabilities.includes("units")) {
    registerComparer({
      id: previewerComparerId(d.name),
      label: d.name,
      match,
      rank: d.rank,
      source: "component",
      load: () => import("./compare/remote").then((m) => m.previewerUnits(d.name)),
    });
  } else unregisterComparer(previewerComparerId(d.name));
}

function remove(name: string): void {
  installed.delete(name);
  unregisterArtifactViewer(previewerViewerId(name));
  unregisterComparer(previewerComparerId(name));
}

function previewerComparerId(name: string): string {
  return `component.compare.${name}`;
}

function sameRegistration(a: PreviewerDescriptor, b: PreviewerDescriptor): boolean {
  return (
    a.version === b.version &&
    a.bundleSha256 === b.bundleSha256 &&
    a.rank === b.rank &&
    a.description === b.description &&
    a.mimeTypes.join() === b.mimeTypes.join() &&
    a.extensions.join() === b.extensions.join() &&
    a.capabilities.join() === b.capabilities.join()
  );
}

/** A descriptor whose browser frame ticket is still valid for a new frame:
 *  re-reads the catalog first when the ticket is near expiry. undefined when
 *  the previewer is no longer installed for this member. */
export function freshPreviewer(name: string, nowMs = Date.now()): Promise<PreviewerDescriptor | undefined> {
  return catalog.fresh(name, nowMs);
}

/** Fills the registry for the public share page from the link's own list (the
 *  previewers its creator can offer for the shared file, each with a frame
 *  ticket), instead of the member catalog the page cannot read. `settled`
 *  resolves when the first lookup answered or failed, or after SHARE_LOOKUP_MS:
 *  the page chooses its viewer then, so a file with a previewer is never shown
 *  as code first. `stop` removes what the lookup registered and gives the
 *  registry its member source back. */
export function startSharePreviewers(link: string): { readonly settled: Promise<void>; readonly stop: () => void } {
  // The desktop shell serves a frame from its own bnbloc-previewer scheme, out of
  // the member's own catalog: a previewer only the share's creator has would not
  // load there. The page keeps its viewers as they were.
  if (inDesktopShell()) return { settled: Promise.resolve(), stop: () => {} };
  const restore = catalog.setSource({ read: () => publicShareApi.previewers(link) });
  const settled = settledWithin(catalog.sync().catch(() => {}), SHARE_LOOKUP_MS);
  return {
    settled,
    stop: () => {
      restore();
      for (const name of [...installed.keys()]) remove(name);
    },
  };
}

/** Starts the catalog sync for a signed-in member (now, then periodically);
 *  returns the stop function. Errors are background best-effort: the next
 *  tick retries and existing registrations stay. */
export function startPreviewerSync(): () => void {
  const tick = () => void syncInstalledPreviewers().catch(() => {});
  tick();
  const stop = visiblePoll(tick, SYNC_INTERVAL_MS);
  return () => {
    stop();
    for (const name of [...installed.keys()]) remove(name);
  };
}
