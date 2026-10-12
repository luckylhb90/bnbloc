import { useEffect, useState } from "react";
import { BookmarkSimpleIcon } from "@phosphor-icons/react";
import { api, type Artifact } from "@/lib/api";
import { contentKindWord } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { isManifestPath, packageDir, packageFiles, parseDraftManifest, tarArchive, type DraftManifest } from "@/lib/contentDrafts";
import { useToast } from "@/ui/Toast";
import { savedActions, savedMessage } from "@/views/library/workshopFlow";
import { loadArtifactDetail, loadVersionBlob, versionMeta } from "./artifactData";
import { errorText } from "@/lib/errors";

// At most this many artifact list pages are read to gather a package's files.
const MAX_PAGES = 5;

interface DraftPackage {
  readonly dir: string;
  readonly manifest: DraftManifest;
}

// draftPackages finds the content packages among a turn's deliverables: a
// component.json of an expert, skill pack or prompt pack inside a directory (a package at
// the workspace root is not offered: it would sweep every deliverable in).
async function draftPackages(roomId: string, artifactIds: readonly string[]): Promise<DraftPackage[]> {
  const out: DraftPackage[] = [];
  for (const id of artifactIds) {
    try {
      const detail = await loadArtifactDetail(roomId, id);
      const a = detail.artifact;
      const dir = packageDir(a.title);
      if (!isManifestPath(a.title) || !dir || out.some((p) => p.dir === dir)) continue;
      const blob = await loadVersionBlob(roomId, a, a.currentVersion, versionMeta(detail, a.currentVersion));
      const manifest = parseDraftManifest(await blob.text());
      if (manifest) out.push({ dir, manifest });
    } catch {
      // An unreadable deliverable is not a package.
    }
  }
  return out;
}

// packDraft gathers the package's files from the room's deliverables
// (latest version of each) into a tar.
async function packDraft(roomId: string, dir: string): Promise<Blob> {
  const all: Artifact[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < MAX_PAGES; page++) {
    const res = await api.artifacts(roomId, { q: dir + "/", limit: 200, cursor });
    all.push(...(res.artifacts ?? []));
    if (!res.nextCursor) break;
    cursor = res.nextCursor;
  }
  const files = packageFiles(all, dir);
  const entries = await Promise.all(
    files.map(async (f) => {
      const blob = await loadVersionBlob(roomId, f.artifact, f.artifact.currentVersion);
      return { path: f.path, data: new Uint8Array(await blob.arrayBuffer()) };
    }),
  );
  return new Blob([tarArchive(entries)], { type: "application/x-tar" });
}

// SaveDraftActions offers "Save as expert" / "Save as skill pack" / "Save as
// prompt pack" under a turn's deliverables when they hold a drafted
// package: it packs the drafted
// files and saves them as the clicking member (a new component, or a new
// version of theirs with that name). The server's validator message shows
// inline.
export function SaveDraftActions({ roomId, artifactIds }: { readonly roomId: string; readonly artifactIds: readonly string[] }) {
  const [packages, setPackages] = useState<DraftPackage[]>([]);
  const key = artifactIds.join(",");
  useEffect(() => {
    let live = true;
    void draftPackages(roomId, key ? key.split(",") : []).then((p) => live && setPackages(p));
    return () => {
      live = false;
    };
  }, [roomId, key]);
  if (packages.length === 0) return null;
  return (
    <div className="flex flex-col gap-1">
      {packages.map((p) => (
        <SaveDraftButton key={p.dir} roomId={roomId} pkg={p} />
      ))}
    </div>
  );
}

function SaveDraftButton({ roomId, pkg }: { readonly roomId: string; readonly pkg: DraftPackage }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const word = contentKindWord(pkg.manifest.kind);
  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      const tar = await packDraft(roomId, pkg.dir);
      const mine = (await contentApi.list(pkg.manifest.kind)).components ?? [];
      const existing = mine.find((c) => c.mine && c.kind === pkg.manifest.kind && c.name === pkg.manifest.name);
      const saved = existing ? await contentApi.newVersionArchive(existing.id, tar) : await contentApi.createArchive(tar);
      const c = saved.component;
      toast.success(savedMessage(c), savedActions(c, roomId, (msg) => toast.info(msg)));
    } catch (e) {
      setError(errorText(e, `Could not save the ${word}`));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex max-w-xl flex-col gap-1">
      <button
        type="button"
        onClick={() => void save()}
        disabled={busy}
        className="tactile inline-flex w-fit items-center gap-1.5 rounded-[var(--radius)] px-2 py-1 text-xs font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent-soft)] disabled:opacity-60"
        title={`Save ${pkg.dir}/ to your Library`}
      >
        <BookmarkSimpleIcon size={14} aria-hidden />
        {busy ? "Saving…" : `Save as ${word}: ${pkg.manifest.displayName ?? pkg.manifest.name}`}
      </button>
      {error && (
        <p role="alert" className="whitespace-pre-wrap rounded-[var(--radius)] bg-[var(--color-danger)]/10 px-2 py-1 text-xs text-[var(--color-danger)]">
          {error}
        </p>
      )}
    </div>
  );
}
