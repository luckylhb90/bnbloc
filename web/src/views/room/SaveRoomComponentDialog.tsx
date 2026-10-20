import { useEffect, useState } from "react";
import { DESCRIBE_ROOM, runJSONAssist, useAssistTask } from "@/lib/assist";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import { applyDependencies, roomDependencies, type RoomDependency } from "@/lib/roomCapture";
import {
  partKey,
  roomContentForm,
  roomDraftPatch,
  roomInclude,
  roomSaveFromComponent,
  roomSaveFromDraft,
  roomSaveProblem,
  scheduleLine,
  toggleRoomPart,
  type RoomPartKind,
  type RoomSaveForm,
} from "@/lib/roomComponents";
import { ROOM_SOURCE_LABELS, type RoomSourceAsk } from "@/lib/roomSources";
import { roomComponentsApi } from "@/lib/roomComponentsApi";
import { FINDING_LABELS, findingId, findingWhere, fixFinding, openFindings, reviewKey, roomFindings, roomReviewInput, type RoomFinding } from "@/lib/roomReview";
import { MaxRoomSourcePurpose, RoomSourceValues } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { AssistButton } from "@/views/AssistButton";
import { RoomCaptureSections } from "@/views/room/RoomCaptureSections";

/** What the dialog saves: a room (its draft, as a new component or a new
 *  version of one of the member's), or a room component (its next
 *  version). */
export type RoomSaveTarget = { readonly roomId: string } | { readonly component: ContentComponent };

const heading = "text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]";
const check = "flex items-start gap-2 rounded-[var(--radius)] px-2 py-1 text-sm hover:bg-[var(--color-surface-2)]";

// SaveRoomComponentDialog is "Save as component" (room components PRD
// §2.1): from a room's header (its owner or an admin) the room's setup as
// the server captured it (GET …/component-draft), or a room component of
// the member's for its next version. The member edits the name and the
// text, unticks parts, declares the sources its tasks ask for, and saves
// through the content form (contentApi.create / newVersion; the room's
// apps and files it ticks ride the save's include). "Fill with AI" (the
// describe-room assist, reading the room's conversation) drafts the name,
// description, category, tags, welcome and the room's instructions, says
// what the room depends on and ticks what it says the package should
// carry (RoomCaptureSections); nothing is saved until they press Save. The same
// assist checks the package's text before it leaves the room (on Save,
// when the text changed since the last check; lib/roomReview.ts): what
// should not leave (a secret, an internal link, a person's name, private
// data) is listed with "Fix" (edit that field, or leave the part out) and
// "Ignore"; "Save anyway" keeps saving the member's decision.
export function SaveRoomComponentDialog({ target, onClose, onSaved }: { readonly target: RoomSaveTarget | null; readonly onClose: () => void; readonly onSaved?: () => void }) {
  const toast = useToast();
  const describe = useAssistTask(DESCRIBE_ROOM);
  const [form, setForm] = useState<RoomSaveForm | null>(null);
  const [left, setLeft] = useState<readonly string[]>([]);
  const [mine, setMine] = useState<ContentComponent[]>([]);
  const [saveAs, setSaveAs] = useState("");
  const [failure, setFailure] = useState("");
  const [busy, setBusy] = useState(false);
  const [filling, setFilling] = useState(false);
  const [findings, setFindings] = useState<readonly RoomFinding[]>([]);
  const [ignored, setIgnored] = useState<readonly string[]>([]);
  const [reviewed, setReviewed] = useState("");
  const [deps, setDeps] = useState<readonly RoomDependency[]>([]);
  const roomId = target && "roomId" in target ? target.roomId : "";
  const component = target && "component" in target ? target.component : null;

  useEffect(() => {
    setFailure("");
    setForm(component ? roomSaveFromComponent(component) : null);
    setSaveAs(component?.id ?? "");
    setLeft([]);
    setFindings([]);
    setIgnored([]);
    setReviewed("");
    setDeps([]);
    let live = true;
    if (component) {
      // The version's instructions are its file: read them in.
      void contentApi
        .version(component.id, component.latestVersion)
        .then((v) => live && v.instructions && setForm((f) => (f && !f.instructions ? { ...f, instructions: v.instructions ?? "" } : f)))
        .catch((e) => live && setFailure(errorText(e, "Could not read the component's instructions")));
    }
    if (!roomId) {
      return () => {
        live = false;
      };
    }
    void Promise.all([
      roomComponentsApi.draft(roomId),
      contentApi
        .list("room")
        .then((r) => (r.components ?? []).filter((c) => c.mine))
        .catch(() => [] as ContentComponent[]),
    ])
      .then(([d, own]) => {
        if (!live) return;
        setForm(roomSaveFromDraft(d));
        setLeft(d.left);
        setMine(own);
      })
      .catch((e) => live && setFailure(errorText(e, "Could not read the room")));
    return () => {
      live = false;
    };
  }, [roomId, component]);

  if (!target) return null;
  const set = (patch: Partial<RoomSaveForm>) => setForm((f) => (f ? { ...f, ...patch } : f));
  const next = mine.find((c) => c.id === saveAs) ?? component;
  const problem = form ? roomSaveProblem(next ? { ...form, name: next.name } : form) : null;

  const open = form ? openFindings(findings, form, ignored) : [];
  // review runs describe-room over f: its draft (answered) and its findings.
  const review = async (f: RoomSaveForm) => {
    const r = await runJSONAssist(describe?.name ?? "", { text: roomReviewInput(f), ...(roomId ? { roomId } : {}) });
    setFindings(roomFindings(r.fields ?? {}, f));
    setDeps(roomDependencies(r.fields ?? {}));
    return r.fields ?? {};
  };

  const fill = async () => {
    if (!describe || !form || filling) return;
    setFilling(true);
    setFailure("");
    try {
      const fields = await review(form);
      const next = applyDependencies({ ...form, ...roomDraftPatch(fields, form) }, roomDependencies(fields));
      setForm(next);
      setReviewed(reviewKey(next));
    } catch (e) {
      setFailure(errorText(e, "Could not fill it in with AI"));
    } finally {
      setFilling(false);
    }
  };

  const fix = (x: RoomFinding) => {
    if (!form) return;
    const fx = fixFinding(form, x);
    if ("edit" in fx) {
      document.getElementById(`room-save-${fx.edit}`)?.focus();
      return;
    }
    setForm(fx.form);
    toast.info(fx.label);
  };

  const submit = async () => {
    if (!form || problem || busy) return;
    setBusy(true);
    setFailure("");
    // The text is checked before it leaves the room, once per change; what
    // the check finds is the member's to fix, ignore or save anyway.
    if (describe && reviewed !== reviewKey(form)) {
      try {
        const fields = await review(form);
        setReviewed(reviewKey(form));
        if (openFindings(roomFindings(fields, form), form, ignored).length > 0) {
          setBusy(false);
          return;
        }
      } catch (e) {
        setReviewed(reviewKey(form));
        setFailure(`${errorText(e, "AI could not check the text")}: press Save again to save it unchecked`);
        setBusy(false);
        return;
      }
    }
    try {
      const f = roomContentForm(next ? { ...form, name: next.name } : form);
      const include = roomInclude(form, roomId);
      const d = next ? await contentApi.newVersion(next.id, f, next.latestVersion, include) : await contentApi.create(f, include);
      toast.success(`${contentLabel(d.component)} v${d.component.latestVersion} is in your Library (Rooms)`);
      onSaved?.();
      onClose();
    } catch (e) {
      setFailure(errorText(e, "Could not save it"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title={component ? `New version of ${contentLabel(component)}` : "Save room as component"}
      description="A room component holds the room's setup and way of working: its description, instructions, channels, scheduled tasks, expert, packs, apps, the files you tick and the tools it needs. Whoever installs it gets a room of their own with the tasks paused; nobody's people, conversations or app data come along."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!form || !!problem || busy}>
            {saveLabel(busy, !!form && reviewed !== reviewKey(form), !!describe, open.length > 0, !!next)}
          </Button>
        </>
      }
    >
      {!form ? (
        failure ? <ErrorBox>{failure}</ErrorBox> : <p className="text-sm text-[var(--color-fg-subtle)]" role="status">Reading the room…</p>
      ) : (
        <div className="flex flex-col gap-4">
          {describe && (
            <div className="flex items-center justify-between gap-3 rounded-[var(--radius-lg)] bg-[var(--color-surface-2)] px-3 py-2">
              <p className="text-xs text-[var(--color-fg-muted)]">
                Read the room and its conversation: draft the listing and the room&apos;s instructions, and say what its way of working depends on. Then edit them.
              </p>
              <AssistButton variant="text" label="Fill with AI" state={filling ? "waiting" : "idle"} onRun={() => void fill()} disabled={busy} />
            </div>
          )}
          {!component && mine.length > 0 && (
            <Field label="Save as" hint="A new component, or the next version of one of yours (rooms made from it keep theirs).">
              <Select value={saveAs} onChange={(e) => setSaveAs(e.target.value)}>
                <option value="">A new component</option>
                {mine.map((c) => (
                  <option key={c.id} value={c.id}>
                    New version of {contentLabel(c)} (now v{c.latestVersion})
                  </option>
                ))}
              </Select>
            </Field>
          )}
          {!next && (
            <Field label="Name" hint="Lower-case letters, digits and hyphens: how the component is referred to.">
              <Input value={form.name} onChange={(e) => set({ name: e.target.value })} autoFocus />
            </Field>
          )}
          <Field label="Display name">
            <Input id="room-save-displayName" value={form.displayName} onChange={(e) => set({ displayName: e.target.value })} />
          </Field>
          <Field label="Description" hint="What a room made from it is for; it becomes the new room's description.">
            <Textarea id="room-save-description" value={form.description} onChange={(e) => set({ description: e.target.value })} rows={2} maxLength={500} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Category">
              <Input value={form.category} onChange={(e) => set({ category: e.target.value })} placeholder="reviews" />
            </Field>
            <Field label="Tags" hint="Separated by commas.">
              <Input value={form.tags} onChange={(e) => set({ tags: e.target.value })} />
            </Field>
          </div>
          <Field label="Welcome" hint="What a new room made from it shows first.">
            <Textarea id="room-save-welcome" value={form.welcome} onChange={(e) => set({ welcome: e.target.value })} rows={2} />
          </Field>
          <RoomCaptureSections
            form={form}
            deps={deps}
            onToggle={(key, on) => setForm((f) => (f ? toggleRoomPart(f, key, on) : f))}
            onInstructions={(instructions) => set({ instructions })}
          />
          <RoomParts form={form} onToggle={(key, on) => setForm((f) => (f ? toggleRoomPart(f, key, on) : f))} />
          <SourcesField
            sources={form.sources}
            onChange={(sources) => set({ sources })}
          />
          {left.length > 0 && (
            <section aria-label="Not included" className="flex flex-col gap-1">
              <span className={heading}>Not included</span>
              <ul className="list-disc pl-5 text-xs text-[var(--color-fg-muted)]">
                {left.map((l) => (
                  <li key={l}>{l}</li>
                ))}
              </ul>
            </section>
          )}
          {open.length > 0 && <Findings form={form} findings={open} onFix={fix} onIgnore={(x) => setIgnored((l) => [...l, findingId(x)])} />}
          {(failure || problem) && <ErrorBox>{failure || problem}</ErrorBox>}
        </div>
      )}
    </Dialog>
  );
}

/** saveLabel is the save button's words: while saving, whether the text
 *  is checked first, and "Save anyway" while findings are open. */
function saveLabel(busy: boolean, unchecked: boolean, checks: boolean, findings: boolean, version: boolean): string {
  if (busy) return checks && unchecked ? "Checking…" : "Saving…";
  if (findings) return "Save anyway";
  return version ? "Save new version" : "Save";
}

/** Findings lists what the AI check found in the package's text, each with
 *  where it is, the words, why, and "Fix" or "Ignore". */
function Findings({
  form,
  findings,
  onFix,
  onIgnore,
}: {
  readonly form: RoomSaveForm;
  readonly findings: readonly RoomFinding[];
  readonly onFix: (x: RoomFinding) => void;
  readonly onIgnore: (x: RoomFinding) => void;
}) {
  return (
    <section aria-label="Before it leaves the room" role="alert" className="flex flex-col gap-2 rounded-[var(--radius-lg)] border border-[var(--color-danger)]/30 bg-[var(--color-danger)]/5 px-3 py-2">
      <span className={heading}>Before it leaves the room (AI)</span>
      {findings.map((x) => (
        <div key={findingId(x)} className="flex flex-col gap-1 text-xs">
          <p>
            <span className="font-medium">{x.kind ? FINDING_LABELS[x.kind] : "Check this"}</span> in {findingWhere(form, x).toLowerCase()}
            {x.quote && <q className="mx-1 break-words text-[var(--color-fg-muted)]">{x.quote}</q>}
          </p>
          {x.why && <p className="text-[var(--color-fg-muted)]">{x.why}</p>}
          <div className="flex gap-2">
            <Button size="sm" variant="outline" onClick={() => onFix(x)}>
              Fix
            </Button>
            <Button size="sm" variant="ghost" onClick={() => onIgnore(x)}>
              Ignore
            </Button>
          </div>
        </div>
      ))}
    </section>
  );
}

/** RoomParts lists every part of the room with a tick box: unticked, it
 *  stays out of the component. */
function RoomParts({ form, onToggle }: { readonly form: RoomSaveForm; readonly onToggle: (key: string, on: boolean) => void }) {
  const groups: [string, { key: string; label: string }[]][] = [
    ["Channels", form.channels.map((c) => ({ key: partKey("channel", c), label: `#${c}` }))],
    ["Scheduled tasks (created paused in a new room)", form.schedules.map((s, i) => ({ key: partKey("schedule", i), label: scheduleLine(s) }))],
    ["Room expert", form.expert ? [{ key: partKey("expert"), label: form.expert.label }] : []],
    ...(
      [
        ["Skill packs", "skill", form.skills],
        ["Prompt packs", "prompt", form.prompts],
        ["Apps (copied in, without their data)", "app", form.apps],
      ] as const
    ).map(([title, kind, parts]): [string, { key: string; label: string }[]] => [title, parts.map((p) => ({ key: partKey(kind as RoomPartKind, p.ref), label: p.label }))]),
  ];
  const shown = groups.filter(([, items]) => items.length > 0);
  if (shown.length === 0) return <p className="text-sm text-[var(--color-fg-subtle)]">The room has nothing to set up beyond its description.</p>;
  return (
    <section aria-label="Include" className="flex flex-col gap-2">
      {shown.map(([title, items]) => (
        <div key={title} className="flex flex-col gap-0.5">
          <span className={heading}>{title}</span>
          {items.map((it) => (
            <label key={it.key} className={check}>
              <input type="checkbox" className="mt-1" checked={!form.off.includes(it.key)} onChange={(e) => onToggle(it.key, e.target.checked)} />
              <span className="min-w-0 break-words">{it.label}</span>
            </label>
          ))}
        </div>
      ))}
    </section>
  );
}

/** SourcesField declares what the room's scheduled tasks ask to read, each
 *  with its purpose; a declaration grants nothing: a room made from the
 *  component waits for its organization admin's consent to that purpose. */
function SourcesField({ sources, onChange }: { readonly sources: readonly RoomSourceAsk[]; readonly onChange: (s: RoomSourceAsk[]) => void }) {
  return (
    <section aria-label="Asks to read" className="flex flex-col gap-0.5">
      <span className={heading}>Asks to read</span>
      {RoomSourceValues.map((name) => {
        const ask = sources.find((s) => s.name === name);
        return (
          <div key={name} className="flex flex-col gap-1">
            <label className={check}>
              <input
                type="checkbox"
                className="mt-1"
                checked={!!ask}
                onChange={(e) => onChange(e.target.checked ? [...sources, { name, purpose: "" }] : sources.filter((x) => x.name !== name))}
              />
              <span className="text-xs text-[var(--color-fg-muted)]">
                {ROOM_SOURCE_LABELS[name]} — a room made from it waits for its organization admin&apos;s consent
              </span>
            </label>
            {ask && (
              <Field label="Purpose" hint="What the room's scheduled tasks read it for. The admin decides on this purpose, and members of the rooms it covers are told.">
                <Textarea
                  id={`room-save-source:${name}`}
                  value={ask.purpose}
                  rows={2}
                  maxLength={MaxRoomSourcePurpose}
                  onChange={(e) => onChange(sources.map((x) => (x.name === name ? { ...x, purpose: e.target.value } : x)))}
                />
              </Field>
            )}
          </div>
        );
      })}
    </section>
  );
}
