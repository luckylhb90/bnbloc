import { useEffect, useState } from "react";
import { runJSONAssist, useAssistTask } from "@/lib/assist";
import type { ComponentSettingField } from "@/lib/componentSettings";
import { desktop } from "@/lib/desktop";
import { errorText } from "@/lib/errors";
import type { SkillCandidates } from "@/lib/localSkills";
import { candidateField, SUGGEST_SKILL_DIRS, suggestionsFrom, suggestRequestText, withFolders } from "@/lib/skillDirs";
import { Button } from "@/ui/Button";
import { useToast } from "@/ui/Toast";
import { AssistButton } from "@/views/AssistButton";

// SkillFolderCandidates is the skills panel's "Folders other tools keep
// skills in": the folders the provider found (paths under the home, a count
// and one skill's name each), ticked by the member and added to the member
// lines setting the provider names (its extra agent skill folders), through
// the member's settings store like a typed value. "Suggest folders with AI"
// (the suggest-skill-dirs assist, when the deployment has one) ticks the
// ones worth adding and says why; without it the list is ticked by hand.

export function SkillFolderCandidates({
  plugin,
  candidates,
  using,
  onAddSource,
  onSaved,
}: {
  readonly plugin: string;
  readonly candidates: SkillCandidates | undefined;
  // The folders already used (sources and agent folders), for the assist.
  readonly using: readonly string[];
  // Adds a folder as a source of skills (the panel's Folders).
  readonly onAddSource: (path: string) => void;
  readonly onSaved: () => void;
}) {
  const toast = useToast();
  const task = useAssistTask(SUGGEST_SKILL_DIRS);
  const [field, setField] = useState<ComponentSettingField | null | undefined>(undefined);
  const [ticked, setTicked] = useState<readonly string[]>([]);
  const [why, setWhy] = useState<Readonly<Record<string, string>>>({});
  const [notes, setNotes] = useState("");
  const [state, setState] = useState<"idle" | "waiting">("idle");
  const [busy, setBusy] = useState(false);
  const folders = candidates?.folders ?? [];
  const setting = candidates?.setting;

  useEffect(() => {
    let cancelled = false;
    desktop
      .componentSettings()
      .then((items) => {
        const item = (items ?? []).find((i) => i.kind === "plugin" && i.name === plugin);
        if (!cancelled) setField(candidateField(setting, item?.fields ?? []));
      })
      .catch(() => {
        if (!cancelled) setField(null);
      });
    return () => {
      cancelled = true;
    };
  }, [plugin, setting]);

  if (folders.length === 0) return null;

  const suggest = async () => {
    if (!task || state !== "idle") return;
    setState("waiting");
    try {
      const r = suggestionsFrom(folders, (await runJSONAssist(task.name, { text: suggestRequestText(using, folders) })).fields);
      setWhy(r.why);
      setNotes(r.notes);
      setTicked(Object.keys(r.why));
    } catch (e) {
      toast.error(errorText(e, "Could not suggest folders"));
    } finally {
      setState("idle");
    }
  };

  const add = async () => {
    if (!field || busy || ticked.length === 0) return;
    const next = withFolders(field.value, ticked);
    if ("why" in next) {
      toast.error(`${field.label}: ${next.why}`);
      return;
    }
    setBusy(true);
    try {
      await desktop.saveComponentSettings("plugin", plugin, { values: { [field.key]: next.value } });
      toast.success(`Added ${ticked.length} folder${ticked.length === 1 ? "" : "s"} to ${field.label}; ${plugin} restarts with them`);
      setTicked([]);
      setWhy({});
      onSaved();
    } catch (e) {
      toast.error(errorText(e, "Could not save the folders"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-1.5 border-t border-[var(--color-border)] pt-3">
      <div className="flex items-center justify-between gap-3">
        <span className="text-sm font-medium">Folders other tools keep skills in</span>
        {task && field && <AssistButton variant="text" label="Suggest folders with AI" state={state} onRun={() => void suggest()} />}
      </div>
      <p className="text-xs text-[var(--color-fg-muted)]">
        {field
          ? `Tick one to add it to ${field.label}: a skill linked from it is placed at that path in your sandbox too. To share the skills a folder holds, add it to Folders.`
          : "To share the skills a folder holds, add it to Folders."}
      </p>
      {folders.map((c) => (
        <div key={c.path} className="flex items-start justify-between gap-3 text-xs">
          <label className="flex min-w-0 items-start gap-2">
            {field && (
              <input
                type="checkbox"
                checked={ticked.includes(c.path)}
                onChange={(e) => setTicked((cur) => (e.target.checked ? [...cur, c.path] : cur.filter((p) => p !== c.path)))}
              />
            )}
            <span className="min-w-0">
              <span className="block truncate font-mono" title={c.path}>
                {c.path}
              </span>
              <span className="block text-[var(--color-fg-subtle)]">
                {c.skills} skill{c.skills === 1 ? "" : "s"}, such as {c.sample}
              </span>
              {why[c.path] && <span className="block text-[var(--color-fg-muted)]">AI: {why[c.path]}</span>}
            </span>
          </label>
          <Button size="sm" variant="ghost" onClick={() => onAddSource(c.path)}>
            Add to Folders
          </Button>
        </div>
      ))}
      {notes && <p className="text-xs text-[var(--color-fg-muted)]">{notes}</p>}
      {field && (
        <span>
          <Button size="sm" variant="outline" disabled={busy || ticked.length === 0} onClick={() => void add()}>
            {busy ? "Saving…" : `Add ticked to ${field.label}`}
          </Button>
        </span>
      )}
    </div>
  );
}
