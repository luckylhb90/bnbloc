import { req } from "@/lib/api";
import {
  frameTicketStale,
  parsePreviewerCatalog,
  previewerViewerId,
  type PreviewerDescriptor,
} from "@/lib/previewerFrame";
import { registerArtifactViewer, unregisterArtifactViewer } from "./registry";
import { registerComparer, unregisterComparer } from "./compare/registry";
import { visiblePoll } from "@/lib/useVisiblePoll";
import { roomPath } from "@/lib/roomId";

// Installed previewer components in the viewer registry. The member catalog
// (GET /v1/components?kind=previewer — rollout + tenant gated server-side) is
// the only source: every installed previewer becomes a "component" viewer
// whose host is RemotePreviewer; a previewer that leaves the catalog
// (uninstall, rollout off, opt-out) is unregistered on the next sync.

const SYNC_INTERVAL_MS = 10 * 60 * 1000;

const installed = new Map<string, PreviewerDescriptor>();
let inflight: Promise<void> | null = null;

/** The current descriptor of an installed previewer. */
export function installedPreviewer(name: string): PreviewerDescriptor | undefined {
  return installed.get(name);
}

/** Re-reads the catalog and reconciles the registry. Concurrent callers
 *  share one request; a failure keeps the previous registrations. */
export function syncInstalledPreviewers(): Promise<void> {
  if (inflight) return inflight;
  inflight = req<unknown>("/v1/components?kind=previewer")
    .then((body) => applyCatalog(parsePreviewerCatalog(body)))
    .finally(() => {
      inflight = null;
    });
  return inflight;
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
export async function freshPreviewer(name: string, nowMs = Date.now()): Promise<PreviewerDescriptor | undefined> {
  const current = installed.get(name);
  if (current && !frameTicketStale(current, nowMs)) return current;
  await syncInstalledPreviewers();
  return installed.get(name);
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

/** Stores a previewer-made PNG as the version's thumbnail (the room service
 *  keeps the first one; later writes are ignored). */
export async function putVersionThumbnail(roomId: string, artifactId: string, version: number, png: ArrayBuffer): Promise<void> {
  const path = roomPath(roomId, "artifacts", artifactId, "versions", version, "thumbnail");
  await req<void>(path, { method: "PUT", headers: { "Content-Type": "image/png" }, body: png, as: "none" });
}
