import { useCallback, useEffect, useRef, useState } from "react";
import { contentApi } from "@/lib/contentComponentsApi";
import { formatBytes } from "@/lib/format";
import { desktop } from "@/lib/desktop";
import { errorText } from "@/lib/errors";
import {
  publishLabel,
  reachCounts,
  skillLine,
  withAllowed,
  withChoice,
  withEnabled,
  withoutAllowed,
  withoutSource,
  withPublished,
  withSource,
  type SkillRow,
  type SkillsProvider,
  type SkillsSettings,
  type SkillsState,
} from "@/lib/localSkills";
import { Button } from "@/ui/Button";
import { Card } from "@/ui/Card";
import { ErrorBox } from "@/ui/ErrorBox";
import { useToast } from "@/ui/Toast";

// LocalSkills (Settings → Skills) lets the member choose which skill folders on
// this computer reach their agents' sandbox and their library. Nothing is shared
// until it is turned on here, and the sandbox only ever sees read-only copies of
// the skills ticked. The work is done by a skills component the member installed
// (any plugin serving bnbloc.skills.provider/v1; the member may use their own),
// so with none installed the section only says so. Desktop shell only.
export function LocalSkills() {
  const inShell = desktop.available();
  const [providers, setProviders] = useState<SkillsProvider[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setProviders(await desktop.skillsProviders());
      setError(null);
    } catch (e) {
      setError(errorText(e, "Could not list the skills components"));
      setProviders([]);
    }
  }, []);
  useEffect(() => {
    if (inShell) void load();
  }, [inShell, load]);

  if (!inShell) return null;
  return (
    <section className="flex flex-col gap-3">
      <div>
        <h2 className="text-sm font-semibold">Skills</h2>
        <p className="mt-0.5 text-sm text-[var(--color-fg-muted)]">
          Choose which skills on this computer your agents can use inside the sandbox, and which to add to your library. Nothing is shared until you turn it on.
        </p>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      {providers && providers.length === 0 && !error && (
        <p className="text-sm text-[var(--color-fg-subtle)]">
          No skills component is installed for you. Install one from{" "}
          <a className="underline" href="#/components">
            Components
          </a>
          , or use your own.
        </p>
      )}
      {(providers ?? []).map((p) => (
        <ProviderCard key={p.plugin} provider={p} reload={load} />
      ))}
    </section>
  );
}

function ProviderCard({ provider, reload }: { readonly provider: SkillsProvider; readonly reload: () => Promise<void> }) {
  const toast = useToast();
  const [state, setState] = useState<SkillsState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  // Edits wait for the previous save: a toggle sent while another is in flight
  // would be built from stale settings.
  const saving = useRef(false);

  const refresh = useCallback(async () => {
    if (!provider.running) return;
    try {
      setState(await desktop.skillsProviderState(provider.plugin));
      setError(null);
    } catch (e) {
      setError(errorText(e, "Could not read the skills"));
    }
  }, [provider.plugin, provider.running]);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  const save = async (next: SkillsSettings, done?: string) => {
    if (saving.current) return;
    saving.current = true;
    try {
      setState(await desktop.saveSkillsSettings(provider.plugin, next));
      setError(null);
      if (done) toast.success(done);
    } catch (e) {
      toast.error(errorText(e, "Could not save"));
      void refresh();
    } finally {
      saving.current = false;
    }
  };

  const addFolder = async () => {
    if (!state) return;
    try {
      const path = await desktop.pickSkillsFolder();
      if (path) await save(withSource(state.settings, path));
    } catch (e) {
      toast.error(errorText(e, "Could not open the folder picker"));
    }
  };

  const publish = async (row: SkillRow) => {
    if (!state || busy) return;
    setBusy(row.name);
    try {
      const packed = await desktop.packSkill(provider.plugin, row.name);
      const published = row.choice.published;
      const file = new File([packed.archive as BlobPart], `${row.name}.tar`, { type: "application/x-tar" });
      const d = await contentApi.importArchive(file, { kind: "skill", name: row.name, existingId: published?.componentId });
      await save(
        withPublished(state.settings, row.name, { hash: packed.hash, componentId: d.component.id, version: d.component.latestVersion }),
        published ? `Published version ${d.component.latestVersion} of ${row.name}` : `${row.name} is in your library`,
      );
    } catch (e) {
      toast.error(errorText(e, `Could not publish ${row.name}`));
    } finally {
      setBusy(null);
    }
  };

  if (!provider.running) {
    return (
      <Card className="flex flex-col gap-1 px-4 py-3">
        <Title provider={provider} />
        <span className="text-xs text-[var(--color-fg-muted)]">{provider.message || `Not running (${provider.state}).`}</span>
      </Card>
    );
  }
  if (!state) {
    return (
      <Card className="flex flex-col gap-1 px-4 py-3">
        <Title provider={provider} />
        {error ? <ErrorBox>{error}</ErrorBox> : <span className="text-xs text-[var(--color-fg-subtle)]">Reading your skills…</span>}
      </Card>
    );
  }

  const s = state.settings;
  const counts = reachCounts(state.skills);
  return (
    <Card className="flex flex-col gap-3 px-4 py-3">
      <div className="flex items-start justify-between gap-3">
        <Title provider={provider} />
        <label className="flex shrink-0 items-center gap-2 text-sm">
          <input type="checkbox" checked={s.enabled} onChange={(e) => void save(withEnabled(s, e.target.checked))} aria-label="Share skills with my sandbox" />
          On
        </label>
      </div>
      {error && <ErrorBox>{error}</ErrorBox>}
      <p className="text-xs text-[var(--color-fg-muted)]">
        {s.enabled
          ? `${counts.own} skill${counts.own === 1 ? "" : "s"} in your sandbox, ${counts.borrowers} for others borrowing your agents. The sandbox sees read-only copies; your folders are never exposed.`
          : "Off: your sandbox sees none of your skills. Turn it on, add a folder, and tick the skills you want."}
      </p>

      <div className="flex flex-col gap-1.5 border-t border-[var(--color-border)] pt-3">
        <div className="flex items-center justify-between gap-3">
          <span className="text-sm font-medium">Folders</span>
          <Button size="sm" variant="ghost" onClick={() => void addFolder()}>
            Add a folder…
          </Button>
        </div>
        {s.sources.length === 0 && <span className="text-xs text-[var(--color-fg-subtle)]">No folder added yet.</span>}
        {state.sources.map((src) => (
          <div key={src.path} className="flex items-center justify-between gap-3">
            <span className="min-w-0 truncate font-mono text-xs" title={src.path}>
              {src.path}
              {src.error && <span className="ml-2 font-sans text-[var(--color-danger)]">{src.error}</span>}
              {src.truncated && <span className="ml-2 font-sans text-[var(--color-fg-subtle)]">showing the first skills only</span>}
            </span>
            <Button size="sm" variant="ghost" onClick={() => void save(withoutSource(s, src.path))}>
              Remove
            </Button>
          </div>
        ))}
        {state.suggested.map((path) => (
          <div key={path} className="flex items-center justify-between gap-3 text-xs text-[var(--color-fg-muted)]">
            <span className="min-w-0 truncate">
              <span className="font-mono">{path}</span> is where npx skills keeps your skills.
            </span>
            <Button size="sm" variant="ghost" onClick={() => void save(withSource(s, path))}>
              Add
            </Button>
          </div>
        ))}
      </div>

      {state.skills.length > 0 && (
        <div className="flex flex-col divide-y divide-[var(--color-border)] border-t border-[var(--color-border)]">
          {state.skills.map((row) => (
            <SkillRowView key={`${row.source}/${row.name}`} row={row} on={s.enabled} busy={busy === row.name} onChoice={(patch) => void save(withChoice(s, row.name, patch))}
              onAllow={(file, hash) => void save(withAllowed(s, row.name, { file, hash }), `${file} allowed for your sandbox`)}
              onDisallow={(file) => void save(withoutAllowed(s, row.name, file))}
              onPublish={() => void publish(row)} />
          ))}
        </div>
      )}
      {state.notes.length > 0 && (
        <ul className="list-disc pl-5 text-xs text-[var(--color-fg-muted)]">
          {state.notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
      <Button size="sm" variant="ghost" className="self-start" onClick={() => void reload().then(refresh)}>
        Rescan
      </Button>
    </Card>
  );
}

function Title({ provider }: { readonly provider: SkillsProvider }) {
  return (
    <span className="min-w-0">
      <span className="block text-sm font-medium">
        {provider.plugin} <span className="text-xs font-normal text-[var(--color-fg-subtle)]">{provider.version}</span>
      </span>
    </span>
  );
}

function SkillRowView({
  row,
  on,
  busy,
  onChoice,
  onAllow,
  onDisallow,
  onPublish,
}: {
  readonly row: SkillRow;
  readonly on: boolean;
  readonly busy: boolean;
  readonly onChoice: (patch: { vm?: boolean; borrowers?: boolean }) => void;
  readonly onAllow: (file: string, hash: string) => void;
  readonly onDisallow: (file: string) => void;
  readonly onPublish: () => void;
}) {
  const usable = row.eligible && !row.hiddenBy;
  return (
    <div className="flex flex-col gap-1.5 py-2.5">
      <div className="flex items-start justify-between gap-3">
        <span className="min-w-0">
          <span className="block truncate text-sm font-medium">{row.name}</span>
          <span className={`block text-xs ${usable ? "text-[var(--color-fg-muted)]" : "text-[var(--color-fg-subtle)]"}`}>{skillLine(row)}</span>
          {usable && (
            <span className="block text-xs text-[var(--color-fg-subtle)]">
              {row.files} file{row.files === 1 ? "" : "s"} · {formatBytes(row.bytes)}
            </span>
          )}
        </span>
        {usable && (
          <Button size="sm" variant={row.changed ? "primary" : "ghost"} disabled={busy || !!row.publishProblem || (!!row.choice.published && !row.changed)} onClick={onPublish}>
            {busy ? "Publishing…" : publishLabel(row)}
          </Button>
        )}
      </div>
      {!row.eligible && !row.hiddenBy && row.finding?.allowable && (
        <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-fg-muted)]">
          <span>Look at {row.finding.file}: if it is a placeholder, allow it for your sandbox (it is never shared with the library).</span>
          <Button size="sm" variant="ghost" onClick={() => onAllow(row.finding!.file, row.finding!.hash)}>
            Allow this file
          </Button>
        </div>
      )}
      {(row.allowed ?? []).map((file) => (
        <div key={file} className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-fg-muted)]">
          <span>{file} is allowed for your sandbox.</span>
          <Button size="sm" variant="ghost" onClick={() => onDisallow(file)}>
            Stop allowing
          </Button>
        </div>
      ))}
      {usable && (
        <div className="flex flex-wrap items-center gap-4 text-sm">
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={row.choice.vm} disabled={!on} onChange={(e) => onChoice({ vm: e.target.checked })} />
            In my sandbox
          </label>
          <label className={`flex items-center gap-2 ${row.choice.vm ? "" : "text-[var(--color-fg-subtle)]"}`}>
            <input type="checkbox" checked={row.choice.borrowers} disabled={!on || !row.choice.vm} onChange={(e) => onChoice({ borrowers: e.target.checked })} />
            For others borrowing my agents
          </label>
        </div>
      )}
    </div>
  );
}
