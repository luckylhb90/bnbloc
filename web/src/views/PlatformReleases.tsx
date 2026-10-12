import { useCallback, useEffect, useState } from "react";
import { PlusCircleIcon } from "@phosphor-icons/react";
import { api, ApiError, type ReleaseEntry, type ReleaseFile, type ReleaseHubMeta } from "@/lib/api";
import { Button } from "@/ui/Button";
import { Badge, Card } from "@/ui/Card";
import { ConfirmDialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { ErrorBox } from "@/ui/ErrorBox";
import { errorText } from "@/lib/errors";

type Channel = "stable" | "internal";
type ReleaseForm = { readonly version: string; readonly channel: Channel; readonly releaseDate: string; readonly notes: string; readonly files: readonly FileForm[] };
type FileForm = { readonly platform: string; readonly arch: string; readonly target: string; readonly name: string; readonly url: string; readonly sha256: string; readonly size: string };
type BuildReleaseResult = { readonly ok: true; readonly body: Omit<ReleaseEntry, "version"> } | { readonly ok: false; readonly error: string };

const CHANNEL_OPTIONS = [
  { value: "stable", label: "Stable" },
  { value: "internal", label: "Internal" },
] as const;
const EMPTY_META: ReleaseHubMeta = { name: "", tagline: "", intro: "" };
const EMPTY_FILE: FileForm = { platform: "", arch: "", target: "", name: "", url: "", sha256: "", size: "" };
const EMPTY_FORM: ReleaseForm = { version: "", channel: "stable", releaseDate: "", notes: "", files: [EMPTY_FILE] };

export function PlatformReleases() {
  const toast = useToast();
  const [meta, setMeta] = useState<ReleaseHubMeta>(EMPTY_META);
  const [releases, setReleases] = useState<readonly ReleaseEntry[]>([]);
  const [form, setForm] = useState<ReleaseForm | null>(null);
  const [editingVersion, setEditingVersion] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [savingMeta, setSavingMeta] = useState(false);
  const [savingRelease, setSavingRelease] = useState(false);
  const [deletingVersion, setDeletingVersion] = useState<string | null>(null);
  const [confirmVersion, setConfirmVersion] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const hub = await api.getPlatformReleases();
      setMeta(hub.project ?? EMPTY_META);
      setReleases(hub.releases ?? []);
    } catch (e: unknown) {
      setLoadError(errorText(e, "failed to load releases"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const patchMeta = (patch: Partial<ReleaseHubMeta>) => setMeta((current) => ({ ...current, ...patch }));
  const patchForm = (patch: Partial<ReleaseForm>) => setForm((current) => (current ? { ...current, ...patch } : current));
  const patchFile = (index: number, patch: Partial<FileForm>) =>
    setForm((current) => current && { ...current, files: current.files.map((file, i) => (i === index ? { ...file, ...patch } : file)) });
  const addFile = () => setForm((current) => current && { ...current, files: [...current.files, EMPTY_FILE] });
  const removeFile = (index: number) => setForm((current) => current && { ...current, files: current.files.filter((_, i) => i !== index) });

  const saveMeta = async () => {
    setSavingMeta(true);
    setLoadError(null);
    try {
      setMeta(await api.putReleaseMeta(meta));
      toast.success("Release intro saved");
    } catch (e: unknown) {
      toast.error(errorText(e, "failed to save release intro"));
    } finally {
      setSavingMeta(false);
    }
  };

  const saveRelease = async () => {
    if (!form) return;
    const built = buildReleaseBody(form);
    if (!built.ok) {
      setFormError(built.error);
      return;
    }
    setSavingRelease(true);
    setFormError(null);
    try {
      await api.putRelease(form.version.trim(), built.body);
      toast.success(`Release ${form.version.trim()} saved`);
      setForm(null);
      setEditingVersion(null);
      await load();
    } catch (e: unknown) {
      const message = e instanceof ApiError && e.status === 400 ? e.message : errorText(e, "failed to save release");
      setFormError(message);
      toast.error(message);
    } finally {
      setSavingRelease(false);
    }
  };

  const deleteRelease = async (version: string) => {
    setDeletingVersion(version);
    setLoadError(null);
    try {
      await api.deleteRelease(version);
      toast.success(`Release ${version} deleted`);
      await load();
    } catch (e: unknown) {
      setLoadError(errorText(e, "failed to delete release"));
    } finally {
      setDeletingVersion(null);
      setConfirmVersion(null);
    }
  };

  if (loading) return <p className="text-sm text-[var(--color-fg-muted)]">Loading releases…</p>;

  return (
    <section className="flex flex-col gap-4">
      {loadError && <ErrorBox>{loadError}</ErrorBox>}
      <Card className="p-4">
        <SectionHeading title="Release Hub intro" subtitle="Shown above the public version history." />
        <div className="mt-4 grid gap-4 md:grid-cols-2">
          <Field label="Project name"><Input value={meta.name} placeholder="bnbloc" onChange={(e) => patchMeta({ name: e.target.value })} /></Field>
          <Field label="Tagline"><Input value={meta.tagline} onChange={(e) => patchMeta({ tagline: e.target.value })} /></Field>
          <Field label="Intro markdown" hint="Rendered as markdown on the read-only Release Hub."><Textarea rows={5} value={meta.intro} onChange={(e) => patchMeta({ intro: e.target.value })} className="md:col-span-2" /></Field>
        </div>
        <div className="mt-4 flex justify-end"><Button variant="primary" onClick={() => void saveMeta()} disabled={savingMeta}>{savingMeta ? "Saving…" : "Save intro"}</Button></div>
      </Card>

      <Card className="p-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <SectionHeading title="Versions" subtitle="Manage public changelog entries and direct download rows." />
          <Button variant="primary" size="sm" onClick={() => { setEditingVersion(null); setForm(EMPTY_FORM); setFormError(null); }}><PlusCircleIcon size={16} weight="fill" /> Add version</Button>
        </div>
        <div className="mt-4 flex flex-col gap-2">
          {releases.length > 0 ? releases.map((release) => (
            <div key={release.version} className="flex flex-wrap items-center gap-3 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
              <span className="tabular font-semibold text-[var(--color-fg)]">v{release.version}</span>
              <Badge tone={release.channel === "stable" ? "accent" : "warning"}>{release.channel}</Badge>
              <span className="text-sm text-[var(--color-fg-muted)]">{release.releaseDate || "No date"}</span>
              <span className="text-sm text-[var(--color-fg-subtle)]">{release.files.length} files</span>
              <div className="ml-auto flex items-center gap-2">
                <Button size="sm" variant="outline" onClick={() => { setEditingVersion(release.version); setForm(formFromRelease(release)); setFormError(null); }}>Edit</Button>
                <Button size="sm" variant="danger" onClick={() => setConfirmVersion(release.version)} disabled={deletingVersion === release.version}>{deletingVersion === release.version ? "Deleting…" : "Delete"}</Button>
              </div>
            </div>
          )) : <div className="rounded-[var(--radius)] border border-dashed border-[var(--color-border)] px-4 py-8 text-center text-sm text-[var(--color-fg-subtle)]">还没有发布版本</div>}
        </div>
      </Card>

      {form && <ReleaseFormCard form={form} editing={editingVersion !== null} error={formError} saving={savingRelease} onPatch={patchForm} onPatchFile={patchFile} onAddFile={addFile} onRemoveFile={removeFile} onSave={saveRelease} onCancel={() => { setForm(null); setEditingVersion(null); setFormError(null); }} />}
      <ConfirmDialog open={confirmVersion !== null} onClose={() => setConfirmVersion(null)} onConfirm={() => { if (confirmVersion) void deleteRelease(confirmVersion); }} title={`Delete release ${confirmVersion ?? ""}?`} description="This removes the version from the public Release Hub." confirmLabel="Delete release" danger />
    </section>
  );
}

function ReleaseFormCard({ form, editing, error, saving, onPatch, onPatchFile, onAddFile, onRemoveFile, onSave, onCancel }: { readonly form: ReleaseForm; readonly editing: boolean; readonly error: string | null; readonly saving: boolean; readonly onPatch: (patch: Partial<ReleaseForm>) => void; readonly onPatchFile: (index: number, patch: Partial<FileForm>) => void; readonly onAddFile: () => void; readonly onRemoveFile: (index: number) => void; readonly onSave: () => Promise<void>; readonly onCancel: () => void }) {
  return (
    <Card className="p-4">
      <SectionHeading title={editing ? `Edit ${form.version}` : "Add version"} subtitle="Release notes are markdown; file URLs must be absolute http(s)." />
      {error && <div className="mt-4"><ErrorBox>{error}</ErrorBox></div>}
      <div className="mt-4 grid gap-4 md:grid-cols-2">
        <Field label="Version" hint="Semver, for example 0.1.2"><Input value={form.version} disabled={editing} onChange={(e) => onPatch({ version: e.target.value })} /></Field>
        <Field label="Channel"><Segment options={CHANNEL_OPTIONS} value={form.channel} onChange={(channel) => onPatch({ channel })} /></Field>
        <Field label="Release date" hint="RFC3339, for example 2026-09-21T00:00:00Z"><Input value={form.releaseDate} onChange={(e) => onPatch({ releaseDate: e.target.value })} /></Field>
        <Field label="Notes markdown"><Textarea rows={6} value={form.notes} onChange={(e) => onPatch({ notes: e.target.value })} className="md:col-span-2" /></Field>
      </div>
      <FileRows files={form.files} onPatch={onPatchFile} onAdd={onAddFile} onRemove={onRemoveFile} />
      <div className="mt-4 flex justify-end gap-2"><Button variant="ghost" onClick={onCancel}>Cancel</Button><Button variant="primary" onClick={() => void onSave()} disabled={saving}>{saving ? "Saving…" : "Save version"}</Button></div>
    </Card>
  );
}

function FileRows({ files, onPatch, onAdd, onRemove }: { readonly files: readonly FileForm[]; readonly onPatch: (index: number, patch: Partial<FileForm>) => void; readonly onAdd: () => void; readonly onRemove: (index: number) => void }) {
  return (
    <div className="mt-5 flex flex-col gap-3">
      <div className="flex items-center justify-between"><SectionHeading title="Files" subtitle="One row per downloadable artifact." /><Button size="sm" variant="outline" onClick={onAdd}>Add file</Button></div>
      {files.map((file, index) => (
        <div key={index} className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] p-3">
          <div className="grid gap-3 md:grid-cols-3">
            <Field label="Platform"><Input value={file.platform} onChange={(e) => onPatch(index, { platform: e.target.value })} /></Field>
            <Field label="Arch"><Input value={file.arch} onChange={(e) => onPatch(index, { arch: e.target.value })} /></Field>
            <Field label="Target"><Input value={file.target} onChange={(e) => onPatch(index, { target: e.target.value })} /></Field>
            <Field label="Name"><Input value={file.name} onChange={(e) => onPatch(index, { name: e.target.value })} /></Field>
            <Field label="URL"><Input value={file.url} onChange={(e) => onPatch(index, { url: e.target.value })} /></Field>
            <Field label="Size"><Input type="number" min={0} value={file.size} onChange={(e) => onPatch(index, { size: e.target.value })} /></Field>
          </div>
          <div className="mt-3 grid gap-3 md:grid-cols-[minmax(0,1fr)_auto] md:items-end">
            <Field label="SHA256"><Input value={file.sha256} onChange={(e) => onPatch(index, { sha256: e.target.value })} /></Field>
            <Button size="sm" variant="outline" onClick={() => onRemove(index)} disabled={files.length === 1}>Remove file</Button>
          </div>
        </div>
      ))}
    </div>
  );
}

function formFromRelease(release: ReleaseEntry): ReleaseForm {
  return { version: release.version, channel: release.channel === "internal" ? "internal" : "stable", releaseDate: release.releaseDate, notes: release.notes, files: release.files.length > 0 ? release.files.map(fileFormFromRelease) : [EMPTY_FILE] };
}

function fileFormFromRelease(file: ReleaseFile): FileForm {
  return { platform: file.platform, arch: file.arch, target: file.target ?? "", name: file.name, url: file.url, sha256: file.sha256 ?? "", size: typeof file.size === "number" ? String(file.size) : "" };
}

function buildReleaseBody(form: ReleaseForm): BuildReleaseResult {
  const version = form.version.trim();
  if (!version) return { ok: false, error: "Version is required." };
  const files: ReleaseFile[] = [];
  for (const row of form.files) {
    const sizeText = row.size.trim();
    const file: ReleaseFile = { platform: row.platform.trim(), arch: row.arch.trim(), name: row.name.trim(), url: row.url.trim() };
    const size = Number(sizeText);
    if (sizeText && (!Number.isFinite(size) || size < 0)) return { ok: false, error: "File size must be a non-negative number." };
    files.push({ ...file, ...(row.target.trim() ? { target: row.target.trim() } : {}), ...(row.sha256.trim() ? { sha256: row.sha256.trim() } : {}), ...(sizeText ? { size } : {}) });
  }
  return { ok: true, body: { channel: form.channel, releaseDate: form.releaseDate.trim(), notes: form.notes, files } };
}

function SectionHeading({ title, subtitle }: { readonly title: string; readonly subtitle: string }) {
  return <div><h2 className="text-sm font-semibold uppercase tracking-wide text-[var(--color-fg)]">{title}</h2><p className="mt-1 text-sm text-[var(--color-fg-muted)]">{subtitle}</p></div>;
}
