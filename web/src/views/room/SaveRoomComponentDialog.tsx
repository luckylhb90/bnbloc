import { useEffect, useState } from "react";
import { DESCRIBE_ROOM, runJSONAssist, useAssistTask } from "@/lib/assist";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import {
  describeRoomInput,
  partKey,
  roomContentForm,
  roomDraftPatch,
  roomSaveFromComponent,
  roomSaveFromDraft,
  roomSaveProblem,
  scheduleLine,
  sourceAsk,
  toggleRoomPart,
  type RoomPartKind,
  type RoomSaveForm,
  type RoomSource,
} from "@/lib/roomComponents";
import { roomComponentsApi } from "@/lib/roomComponentsApi";
import { RoomSourceValues } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { ErrorBox } from "@/ui/ErrorBox";
import { Field, Input, Textarea } from "@/ui/Input";
import { Select } from "@/ui/Select";
import { useToast } from "@/ui/Toast";
import { AssistButton } from "@/views/AssistButton";

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
// through the content form (contentApi.create / newVersion). "Fill with
// AI" (the describe-room assist) drafts the name, description, category,
// tags and welcome; nothing is saved until they press Save.
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
  const roomId = target && "roomId" in target ? target.roomId : "";
  const component = target && "component" in target ? target.component : null;

  useEffect(() => {
    setFailure("");
    setForm(component ? roomSaveFromComponent(component) : null);
    setSaveAs(component?.id ?? "");
    setLeft([]);
    if (!roomId) return;
    let live = true;
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

  const fill = async () => {
    if (!describe || !form || filling) return;
    setFilling(true);
    setFailure("");
    try {
      const r = await runJSONAssist(describe.name, { text: describeRoomInput(form), ...(roomId ? { roomId } : {}) });
      setForm((f) => (f ? { ...f, ...roomDraftPatch(r.fields ?? {}, f) } : f));
    } catch (e) {
      setFailure(errorText(e, "Could not fill it in with AI"));
    } finally {
      setFilling(false);
    }
  };

  const submit = async () => {
    if (!form || problem || busy) return;
    setBusy(true);
    setFailure("");
    try {
      const f = roomContentForm(next ? { ...form, name: next.name } : form);
      const d = next ? await contentApi.newVersion(next.id, f, next.latestVersion) : await contentApi.create(f);
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
      description="A room component holds the room's setup: its description, channels, scheduled tasks, expert, packs and apps. Whoever installs it gets a room of their own with the tasks paused; nobody's people, conversations or data come along."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={!form || !!problem || busy}>
            {busy ? "Saving…" : next ? "Save new version" : "Save"}
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
              <p className="text-xs text-[var(--color-fg-muted)]">Draft the name, description, category, tags and welcome from the room and its setup, then edit them.</p>
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
            <Input value={form.displayName} onChange={(e) => set({ displayName: e.target.value })} />
          </Field>
          <Field label="Description" hint="What a room made from it is for; it becomes the new room's description.">
            <Textarea value={form.description} onChange={(e) => set({ description: e.target.value })} rows={2} maxLength={500} />
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
            <Textarea value={form.welcome} onChange={(e) => set({ welcome: e.target.value })} rows={2} />
          </Field>
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
          {(failure || problem) && <ErrorBox>{failure || problem}</ErrorBox>}
        </div>
      )}
    </Dialog>
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

/** SourcesField declares what the room's scheduled tasks ask to read; a
 *  declaration grants nothing (the organization admin's consent does,
 *  once it exists). */
function SourcesField({ sources, onChange }: { readonly sources: readonly RoomSource[]; readonly onChange: (s: RoomSource[]) => void }) {
  return (
    <section aria-label="Asks to read" className="flex flex-col gap-0.5">
      <span className={heading}>Asks to read</span>
      {RoomSourceValues.map((s) => (
        <label key={s} className={check}>
          <input
            type="checkbox"
            className="mt-1"
            checked={sources.includes(s)}
            onChange={(e) => onChange(e.target.checked ? [...sources, s] : sources.filter((x) => x !== s))}
          />
          <span className="text-xs text-[var(--color-fg-muted)]">{sourceAsk(s)}</span>
        </label>
      ))}
    </section>
  );
}
