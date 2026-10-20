import { useEffect, useState } from "react";
import { runJSONAssist, useAssistTask } from "@/lib/assist";
import { cleanTags, splitList, tagProblem } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { desktop } from "@/lib/desktop";
import { errorText } from "@/lib/errors";
import type { PublishedSkill, SkillRow } from "@/lib/localSkills";
import { REVIEW_SKILL, skillReviewFrom, skillReviewText, tarFiles, type SkillEntry, type SkillReview } from "@/lib/skillReview";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { AssistButton } from "@/views/AssistButton";

// SkillPublishDialog is the publish step of a local skill (the skills
// panel's "Add to my library" / "Publish changes"): the skill is packed by
// its provider, the member sees its files and sets the library entry (name,
// display name, description, tags), and "Review with AI" (the review-skill
// assist, when the deployment has one) reads SKILL.md and the file list,
// says what to fix first (a secret or a personal path left in, an unclear
// trigger, a missing step) and proposes an entry, applied field by field.
// Publishing stays the member's: nothing is imported until they press it.

interface Packed {
  readonly hash: string;
  readonly archive: Uint8Array;
  readonly files: number;
  readonly text: string;
}

type Form = { -readonly [K in keyof SkillEntry]: K extends "tags" ? string : SkillEntry[K] };

export function SkillPublishDialog({
  plugin,
  row,
  onClose,
  onPublished,
}: {
  readonly plugin: string;
  readonly row: SkillRow | null;
  readonly onClose: () => void;
  readonly onPublished: (row: SkillRow, published: PublishedSkill, done: string) => void;
}) {
  if (!row) return null;
  return <PublishStep key={`${row.source}/${row.name}`} plugin={plugin} row={row} onClose={onClose} onPublished={onPublished} />;
}

function PublishStep({
  plugin,
  row,
  onClose,
  onPublished,
}: {
  readonly plugin: string;
  readonly row: SkillRow;
  readonly onClose: () => void;
  readonly onPublished: (row: SkillRow, published: PublishedSkill, done: string) => void;
}) {
  const review = useAssistTask(REVIEW_SKILL);
  const existing = row.choice.published;
  const [packed, setPacked] = useState<Packed | null>(null);
  const [failure, setFailure] = useState("");
  const [form, setForm] = useState<Form>({ name: row.name, displayName: "", description: row.description, tags: "" });
  const [reviewed, setReviewed] = useState<SkillReview | null>(null);
  const [reviewing, setReviewing] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    desktop
      .packSkill(plugin, row.name)
      .then((p) => {
        const files = tarFiles(p.archive);
        if (!cancelled) setPacked({ hash: p.hash, archive: p.archive, files: files.length, text: skillReviewText(row.name, files) });
      })
      .catch((e: unknown) => {
        if (!cancelled) setFailure(errorText(e, `Could not pack ${row.name}`));
      });
    return () => {
      cancelled = true;
    };
  }, [plugin, row.name]);

  const tags = cleanTags(splitList(form.tags));
  const problem = tagProblem(tags);

  const runReview = async () => {
    if (!review || !packed || reviewing) return;
    setReviewing(true);
    setFailure("");
    try {
      setReviewed(skillReviewFrom((await runJSONAssist(review.name, { text: packed.text })).fields));
    } catch (e) {
      setFailure(errorText(e, "Could not review it"));
    } finally {
      setReviewing(false);
    }
  };

  const publish = async () => {
    if (!packed || busy || problem) return;
    setBusy(true);
    setFailure("");
    try {
      const file = new File([packed.archive as BlobPart], `${row.name}.tar`, { type: "application/x-tar" });
      const d = await contentApi.importArchive(file, {
        kind: "skill",
        name: form.name,
        existingId: existing?.componentId,
        displayName: form.displayName,
        description: form.description,
        tags,
      });
      onPublished(row, { hash: packed.hash, componentId: d.component.id, version: d.component.latestVersion }, existing ? `Published version ${d.component.latestVersion} of ${row.name}` : `${row.name} is in your library`);
      onClose();
    } catch (e) {
      setFailure(errorText(e, `Could not publish ${row.name}`));
    } finally {
      setBusy(false);
    }
  };

  const set = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));
  const proposed = reviewed?.proposed ?? {};
  const apply = (key: keyof Form) => {
    const v = proposed[key];
    if (v !== undefined) set({ [key]: Array.isArray(v) ? v.join(", ") : v } as Partial<Form>);
  };

  return (
    <Dialog
      open
      onClose={onClose}
      title={existing ? `Publish changes to ${row.name}` : `Add ${row.name} to your library`}
      description="Your library keeps a copy of the skill as it is now. Check what it holds and how it is described before you publish it."
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!packed || busy || !!problem} onClick={() => void publish()}>
            {busy ? "Publishing…" : "Publish"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {failure && <ErrorBox>{failure}</ErrorBox>}
        <p className="text-xs text-[var(--color-fg-muted)]">{packed ? `${packed.files} file${packed.files === 1 ? "" : "s"} from ${row.source}.` : "Packing the skill…"}</p>
        {review && (
          <div className="flex flex-col gap-2">
            <span>
              <AssistButton variant="text" label="Review with AI" state={reviewing ? "waiting" : "idle"} onRun={() => void runReview()} disabled={!packed} />
            </span>
            {reviewed && (reviewed.findings.length > 0 ? (
              <ul className="list-disc pl-5 text-xs text-[var(--color-warning)]">
                {reviewed.findings.map((f) => (
                  <li key={f}>{f}</li>
                ))}
              </ul>
            ) : (
              <p className="text-xs text-[var(--color-fg-muted)]">Nothing to fix was found.</p>
            ))}
          </div>
        )}
        {!existing && (
          <EntryField label="Name in the library" proposed={proposed.name} onApply={() => apply("name")}>
            <Input value={form.name} onChange={(e) => set({ name: e.target.value })} className="font-mono" />
          </EntryField>
        )}
        <EntryField label="Display name" proposed={proposed.displayName} onApply={() => apply("displayName")}>
          <Input value={form.displayName} onChange={(e) => set({ displayName: e.target.value })} placeholder="From SKILL.md when empty" />
        </EntryField>
        <EntryField label="Description" proposed={proposed.description} onApply={() => apply("description")}>
          <Textarea rows={3} value={form.description} onChange={(e) => set({ description: e.target.value })} placeholder="From SKILL.md when empty" />
        </EntryField>
        <EntryField label="Tags" proposed={proposed.tags?.join(", ")} onApply={() => apply("tags")} error={problem ?? undefined}>
          <Input value={form.tags} onChange={(e) => set({ tags: e.target.value })} placeholder="Comma-separated" />
        </EntryField>
      </div>
    </Dialog>
  );
}

// EntryField is one field of the entry and, after a review, what the AI
// proposed for it with "Apply".
function EntryField({
  label,
  proposed,
  onApply,
  error,
  children,
}: {
  readonly label: string;
  readonly proposed?: string;
  readonly onApply: () => void;
  readonly error?: string;
  readonly children: React.ReactNode;
}) {
  return (
    <Field label={label} error={error}>
      {children}
      {proposed && (
        <span className="flex items-start gap-2 text-xs text-[var(--color-fg-muted)]">
          <span className="min-w-0 flex-1">AI proposes: {proposed}</span>
          <Button size="sm" variant="ghost" onClick={onApply}>
            Apply
          </Button>
        </span>
      )}
    </Field>
  );
}
