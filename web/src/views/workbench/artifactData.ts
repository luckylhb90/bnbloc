import { useEffect, useRef, useState } from "react";
import { api, type Artifact, type ArtifactVersion } from "@/lib/api";
import { copyName, inferMime, versionFacts } from "@/lib/artifactFiles";
import { workGalleryApi } from "@/lib/workGalleryApi";
import { myAppsApi } from "@/lib/myAppsApi";
import { fileInfoFromFacts } from "@/views/artifact/fileInfo";
import type { ArtifactFileInfo } from "@/views/artifact/registry";

export type { ArtifactDetail } from "../../lib/wire.gen.ts";
import type { ArtifactDetail } from "../../lib/wire.gen.ts";
import { errorText } from "@/lib/errors";
import { coverLines, renderTextCover } from "../../../previewer-sdk/src/cover.ts";

// Shared artifact data for the workbench, chat file cards and the
// deliverables view:
//   - artifact detail (metadata + version list), cached per artifact and
//     invalidated by the room stream's artifact_updated, so N cards and an
//     open tab cost one request, not N;
//   - version bytes, cached by (room, artifact, version) — versions are
//     immutable, so a cached blob is never stale;
//   - card images (stored thumbnail or the image itself) as object URLs.
// Everything is fetched through the authenticated transport; nothing here
// puts a credential into a URL.

const MAX_DETAILS = 300;
const details = new Map<string, Promise<ArtifactDetail>>();
const detailListeners = new Set<(key: string | null) => void>();

function detailKey(roomId: string, artifactId: string): string {
  return `${roomId}\u0000${artifactId}`;
}

export function loadArtifactDetail(roomId: string, artifactId: string): Promise<ArtifactDetail> {
  const key = detailKey(roomId, artifactId);
  const cached = details.get(key);
  if (cached) return cached;
  const pending = api
    .artifactDetail(roomId, artifactId)
    .then((res) => ({ artifact: res.artifact, versions: res.versions ?? [] }));
  // A failure is not cached: the next consumer retries.
  pending.catch(() => {
    if (details.get(key) === pending) details.delete(key);
  });
  details.set(key, pending);
  if (details.size > MAX_DETAILS) details.delete(details.keys().next().value as string);
  return pending;
}

/** Drops cached detail (one artifact, or the whole room on reconnect) and
 *  tells mounted consumers to refetch. */
export function invalidateArtifacts(roomId: string, artifactId?: string): void {
  if (artifactId) {
    const key = detailKey(roomId, artifactId);
    details.delete(key);
    for (const l of detailListeners) l(key);
    return;
  }
  const prefix = `${roomId}\u0000`;
  for (const key of [...details.keys()]) if (key.startsWith(prefix)) details.delete(key);
  for (const l of detailListeners) l(null);
}

/** Stores a cover a viewer drew of a room version as its thumbnail
 *  (docs/prd/2026-10-16-file-covers.md), when the version has none: the
 *  room keeps the first and lets a higher-ranked source replace it. The one
 *  place a viewer's cover reaches a room (ArtifactBody's default sink). */
export async function storeRoomCover(roomId: string, artifactId: string, version: number, png: ArrayBuffer, source: "builtin" | "previewer"): Promise<void> {
  const detail = await loadArtifactDetail(roomId, artifactId);
  const meta = detail.versions.find((v) => v.version === version);
  if (!meta || meta.thumbnail) return;
  await api.putArtifactThumbnail(roomId, artifactId, version, png, source);
  invalidateArtifacts(roomId, artifactId);
}

/** Artifact detail for a mounted consumer; `enabled` defers the fetch (a
 *  card off-screen). Keeps the last detail while a refetch runs. */
export function useArtifactDetail(
  roomId: string,
  artifactId: string | null,
  enabled = true,
): { detail: ArtifactDetail | null; error: boolean } {
  const [rev, setRev] = useState(0);
  const [state, setState] = useState<{ id: string | null; detail: ArtifactDetail | null; error: boolean }>({
    id: null,
    detail: null,
    error: false,
  });
  useEffect(() => {
    if (!artifactId) return;
    const mine = detailKey(roomId, artifactId);
    const listener = (key: string | null) => {
      if (key === null || key === mine) setRev((n) => n + 1);
    };
    detailListeners.add(listener);
    return () => {
      detailListeners.delete(listener);
    };
  }, [roomId, artifactId]);
  useEffect(() => {
    if (!artifactId || !enabled) return;
    let cancelled = false;
    loadArtifactDetail(roomId, artifactId)
      .then((detail) => {
        if (!cancelled) setState({ id: artifactId, detail, error: false });
      })
      .catch(() => {
        if (!cancelled) setState({ id: artifactId, detail: null, error: true });
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, artifactId, enabled, rev]);
  if (state.id !== artifactId) return { detail: null, error: false };
  return { detail: state.detail, error: state.error };
}

export function fileInfoFor(artifact: Artifact, version: number, meta?: ArtifactVersion): ArtifactFileInfo {
  return fileInfoFromFacts(versionFacts(artifact, meta), version, meta?.sizeBytes);
}

export function versionMeta(detail: ArtifactDetail | null, version: number): ArtifactVersion | undefined {
  return detail?.versions.find((v) => v.version === version);
}

// --- version bytes ---

const MAX_BLOBS = 16;
const blobs = new Map<string, Promise<Blob>>();

/** One version's bytes. Inline versions come from the version route (works
 *  against every server); blob-backed versions from the content route. The
 *  blob is typed with the effective MIME type (an SVG must be image/svg+xml
 *  to render as an image). */
export function loadVersionBlob(
  roomId: string,
  artifact: Artifact,
  version: number,
  meta?: ArtifactVersion,
): Promise<Blob> {
  const key = `${roomId}\u0000${artifact.artifactId}\u0000${version}`;
  const cached = blobs.get(key);
  if (cached) return cached;
  const mime = inferMime(versionFacts(artifact, meta)) || "application/octet-stream";
  const pending = (async () => {
    if (meta?.storage === "blob") return api.artifactContent(roomId, artifact.artifactId, version);
    const res = await api.artifactVersion(roomId, artifact.artifactId, version);
    if (res.version.storage === "blob") return api.artifactContent(roomId, artifact.artifactId, version);
    return new Blob([res.version.content ?? ""], { type: mime });
  })().then((blob) => (blob.type === mime ? blob : blob.slice(0, blob.size, mime)));
  pending.catch(() => {
    if (blobs.get(key) === pending) blobs.delete(key);
  });
  blobs.set(key, pending);
  if (blobs.size > MAX_BLOBS) blobs.delete(blobs.keys().next().value as string);
  return pending;
}

/** The bytes of one version for a mounted viewer. */
export function useVersionBlob(
  roomId: string,
  artifact: Artifact | null,
  version: number,
  meta?: ArtifactVersion,
): { blob: Blob | null; error: string } {
  const [state, setState] = useState<{ key: string; blob: Blob | null; error: string }>({ key: "", blob: null, error: "" });
  // The bytes are identified by artifact, version and storage; the latest
  // objects are read through a ref so a detail refetch does not reload them.
  const key = artifact && version > 0 ? `${artifact.artifactId}:${version}:${meta?.storage ?? ""}` : "";
  const latest = useRef({ artifact, meta, version });
  latest.current = { artifact, meta, version };
  useEffect(() => {
    const { artifact: a, meta: m, version: v } = latest.current;
    if (!key || !a || v <= 0) return;
    let cancelled = false;
    loadVersionBlob(roomId, a, v, m)
      .then((blob) => {
        if (!cancelled) setState({ key, blob, error: "" });
      })
      .catch((e: unknown) => {
        if (!cancelled) setState({ key, blob: null, error: errorText(e, "Could not load this file.") });
      });
    return () => {
      cancelled = true;
    };
  }, [roomId, key]);
  return state.key === key ? { blob: state.blob, error: state.error } : { blob: null, error: "" };
}

// --- card images ---

const MAX_CARD_IMAGES = 80;
const cardImages = new Map<string, Promise<string>>();

/** Object URL of a card image: the stored thumbnail ("thumbnail"), the image
 *  itself ("content") or a text file's first lines ("excerpt"). Evicted URLs
 *  are revoked; an <img> that already decoded keeps its pixels. */
export type CardImageSource = "thumbnail" | "content" | "excerpt";

/** Text files above this are not read just to draw a card. */
export const COVER_TEXT_MAX_BYTES = 512 * 1024;
const COVER_TEXT_READ_CHARS = 64 * 1024;

/** A text file's cover: its first lines drawn as a page. Made here, for this
 *  session, and not stored: a previewer that renders the file better sends
 *  its own cover when it is opened, and that is the one worth keeping. */
async function excerptCover(roomId: string, artifact: Artifact, meta: ArtifactVersion): Promise<Blob> {
  const blob = await loadVersionBlob(roomId, artifact, meta.version, meta);
  const lines = coverLines((await blob.slice(0, COVER_TEXT_READ_CHARS).text()).slice(0, COVER_TEXT_READ_CHARS));
  if (lines.length === 0) throw new Error("nothing to draw");
  return new Blob([await renderTextCover(lines)], { type: "image/png" });
}

export function loadCardImage(
  roomId: string,
  artifact: Artifact,
  meta: ArtifactVersion,
  source: CardImageSource,
): Promise<string> {
  return cachedCardImage(`${roomId}\u0000${artifact.artifactId}\u0000${meta.version}\u0000${source}`, () =>
    source === "thumbnail"
      ? api.artifactThumbnail(roomId, artifact.artifactId, meta.version)
      : source === "excerpt"
        ? excerptCover(roomId, artifact, meta)
        : loadVersionBlob(roomId, artifact, meta.version, meta),
  );
}

/** Object URL of an app's cover (its site version's cover image), in the
 *  same cache; it rejects with the gateway's 404/413/415 when the site has
 *  no cover it serves (the card then draws lib/appCover's). */
export function loadAppCover(roomId: string, artifactId: string, version: number): Promise<string> {
  return cachedCardImage(`${roomId}\u0000${artifactId}\u0000${version}\u0000cover`, () => myAppsApi.cover(roomId, artifactId, version));
}

/** Object URL of a work version's cover (the gallery's cards;
 *  coverUrl names the component and the version), in the same cache. */
export function loadComponentCover(coverUrl: string): Promise<string> {
  return cachedCardImage(`component\u0000${coverUrl}`, () => workGalleryApi.cover(coverUrl));
}

function cachedCardImage(key: string, load: () => Promise<Blob>): Promise<string> {
  const cached = cardImages.get(key);
  if (cached) return cached;
  const pending = load().then((blob) => URL.createObjectURL(blob));
  pending.catch(() => {
    if (cardImages.get(key) === pending) cardImages.delete(key);
  });
  cardImages.set(key, pending);
  if (cardImages.size > MAX_CARD_IMAGES) {
    const oldest = cardImages.keys().next().value as string;
    void cardImages.get(oldest)?.then((url) => URL.revokeObjectURL(url)).catch(() => {});
    cardImages.delete(oldest);
  }
  return pending;
}

/** Publishes an earlier version's content as the next version: the one
 *  restore, for the version picker's "Restore" and a change card's Undo
 *  (docs/prd/2026-10-14-select-edit-review.md rule 4). Built on the current
 *  version, so nothing published since is lost. Resolves to the new version. */
export async function restoreVersion(roomId: string, artifact: Artifact, version: number, meta: ArtifactVersion | undefined, summary: string): Promise<number> {
  const bytes = await loadVersionBlob(roomId, artifact, version, meta);
  const base = artifact.currentVersion;
  const res =
    meta?.storage === "blob"
      ? await api.publishArtifactFile(roomId, artifact.artifactId, { file: bytes, fileName: artifact.fileName || artifact.title, baseVersion: base, summary })
      : await api.publishArtifactVersion(roomId, artifact.artifactId, { baseVersion: base, content: await bytes.text(), summary });
  invalidateArtifacts(roomId, artifact.artifactId);
  return res.artifact.currentVersion;
}

/** Copies a file's version into a new file of the same room, kind and
 *  channel ("<name> copy"): the one copy, for the workbench's Copy and for
 *  any file kind (docs/prd/2026-10-17-image-generation-redesign.md §2.4).
 *  Its first version's summary says where it came from. Resolves to the
 *  new file's id. */
export async function copyArtifact(roomId: string, artifact: Artifact, version: number, meta: ArtifactVersion | undefined): Promise<string> {
  const bytes = await loadVersionBlob(roomId, artifact, version, meta);
  const name = copyName(artifact.fileName || artifact.title);
  const summary = `Copied from ${artifact.fileName || artifact.title} v${version}`;
  const created = await api.createArtifact(roomId, {
    kind: artifact.kind,
    title: copyName(artifact.title),
    fileName: artifact.fileName ? name : undefined,
    channelId: artifact.channelId || undefined,
    language: artifact.language,
    ...(meta?.storage === "blob" ? {} : { content: await bytes.text(), summary }),
  });
  const id = created.artifact.artifactId;
  if (meta?.storage === "blob") await api.publishArtifactFile(roomId, id, { file: bytes, fileName: name, baseVersion: 0, summary });
  invalidateArtifacts(roomId);
  return id;
}
