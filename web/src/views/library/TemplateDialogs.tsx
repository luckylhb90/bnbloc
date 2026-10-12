import { useEffect, useState } from "react";
import { Select } from "@/ui/Select";
import { TrashIcon } from "@phosphor-icons/react";
import { ApiError } from "@/lib/api";
import { isSubmitEnter } from "@/lib/keys";
import { useAuth } from "@/lib/auth";
import { roomIdProblem } from "@/lib/roomId";
import {
  emptyTemplateBody,
  parseAgentLines,
  parseLines,
  rememberTemplateStart,
  templateSummary,
  type RoomTemplate,
  type RoomTemplateBody,
  type TemplateContentUse,
  type TemplateSchedule,
  type TemplateVisibility,
} from "@/lib/roomTemplates";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { roomTemplatesApi } from "@/lib/roomTemplatesApi";
import { describeRecurrence, recipientLabel } from "@/lib/schedules";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { useDisplayNames } from "@/lib/useDisplayName";
import { errorText } from "@/lib/errors";
import { agentHandle } from "@/lib/roomAgents";

export const TEMPLATE_VISIBILITY = [
  { value: "private" as const, label: "Only me" },
  { value: "tenant" as const, label: "My organization" },
];

const selectClass =
  "h-10 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm text-[var(--color-fg)] focus:outline-none focus:border-[var(--color-accent)]";

// TemplateEditorDialog creates a template (template = "new") or edits the
// author's own template. Lists are edited one entry per line; prompt packs
// (the new room's starters come from them) are picked from the member's
// Library; schedules come only from "Save as template" and can be removed
// here, not authored.
export function TemplateEditorDialog({
  template,
  onClose,
  onSaved,
}: {
  readonly template: RoomTemplate | "new" | null;
  readonly onClose: () => void;
  readonly onSaved?: (t: RoomTemplate) => void;
}) {
  const toast = useToast();
  const open = template !== null;
  const editing = template && template !== "new" ? template : null;
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [visibility, setVisibility] = useState<TemplateVisibility>("private");
  const [roomDescription, setRoomDescription] = useState("");
  const [channels, setChannels] = useState("");
  const [packs, setPacks] = useState<TemplateContentUse[]>([]);
  const [library, setLibrary] = useState<ContentComponent[] | null>(null);
  const [members, setMembers] = useState("");
  const [agents, setAgents] = useState("");
  const [schedules, setSchedules] = useState<TemplateSchedule[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (!open) return;
    const b: RoomTemplateBody = editing?.body ?? emptyTemplateBody();
    setName(editing?.name ?? "");
    setDescription(editing?.description ?? "");
    setVisibility(editing?.visibility ?? "private");
    setRoomDescription(b.roomDescription ?? "");
    setChannels(b.channels.join("\n"));
    setPacks(b.promptPacks);
    setMembers(b.defaultMembers.join("\n"));
    setAgents(b.suggestedAgents.map(agentHandle).join("\n"));
    setSchedules(b.schedules);
    setErr("");
  }, [open, editing]);

  useEffect(() => {
    if (!open) return;
    let live = true;
    setLibrary(null);
    contentApi
      .list("prompt")
      .then((r) => live && setLibrary(r.components ?? []))
      .catch(() => live && setLibrary([]));
    return () => {
      live = false;
    };
  }, [open]);

  // The packs offered: the member's Library, then any the template names
  // that the Library no longer lists (shown by id, so they can be removed).
  const packOptions = [
    ...(library ?? []).map((c) => ({ id: c.id, label: contentLabel(c) })),
    ...packs.filter((p) => library && !library.some((c) => c.id === p.componentId)).map((p) => ({ id: p.componentId, label: p.componentId })),
  ];
  const togglePack = (id: string, on: boolean) =>
    setPacks((list) => (on ? [...list, { componentId: id }] : list.filter((p) => p.componentId !== id)));

  const submit = async () => {
    if (busy) return;
    if (!name.trim()) {
      setErr("Name is required");
      return;
    }
    const parsedAgents = parseAgentLines(agents);
    if (parsedAgents.invalid.length) {
      setErr(`Not an agent handle (owner/provider): ${parsedAgents.invalid.join(", ")}`);
      return;
    }
    const body: RoomTemplateBody = {
      roomDescription: roomDescription.trim(),
      channels: parseLines(channels),
      promptPacks: packs,
      suggestedAgents: parsedAgents.agents,
      defaultMembers: parseLines(members),
      schedules,
    };
    setBusy(true);
    setErr("");
    try {
      const input = { name: name.trim(), description: description.trim(), visibility, body };
      const saved = editing ? await roomTemplatesApi.update(editing.templateId, input) : await roomTemplatesApi.create(input);
      toast.success(editing ? "Template updated" : "Template created");
      onSaved?.(saved);
      onClose();
    } catch (e) {
      const msg = errorText(e, "Could not save the template");
      setErr(msg);
      toast.error(e instanceof ApiError && e.status === 403 ? "You can only edit your own templates" : errorText(e, "Could not save the template"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      size="lg"
      title={editing ? "Edit template" : "New room template"}
      description="A reusable room setup. Suggested agents are hints only — nobody's agent is authorized by a template."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || !name.trim()}>
            {busy ? "Saving…" : "Save template"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Name" error={err || undefined}>
          <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Weekly design review" />
        </Field>
        <Field label="Description" hint="Optional — when to use this template">
          <Input value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <Field label="Visibility">
          <Segment options={TEMPLATE_VISIBILITY} value={visibility} onChange={setVisibility} />
        </Field>
        <Field label="Room description">
          <Input value={roomDescription} onChange={(e) => setRoomDescription(e.target.value)} placeholder="What the new room is for" />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Channels" hint="One per line (every room also gets “general”)">
            <Textarea rows={4} value={channels} onChange={(e) => setChannels(e.target.value)} placeholder={"design\nops"} />
          </Field>
          <Field label="Default members" hint="User ids, one per line — added as members">
            <Textarea rows={4} value={members} onChange={(e) => setMembers(e.target.value)} />
          </Field>
        </div>
        <div className="flex flex-col gap-1">
          <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
            Prompt packs · turned on in new rooms, their starter prompts included
          </span>
          {library === null ? (
            <p className="text-sm text-[var(--color-fg-subtle)]">Loading prompt packs…</p>
          ) : packOptions.length === 0 ? (
            <p className="text-sm text-[var(--color-fg-subtle)]">No prompt packs in your Library.</p>
          ) : (
            <div className="flex max-h-48 flex-col overflow-y-auto">
              {packOptions.map((o) => (
                <label key={o.id} className="flex items-start gap-2 rounded-[var(--radius)] px-2 py-1.5 text-sm hover:bg-[var(--color-surface-2)]">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={packs.some((p) => p.componentId === o.id)}
                    onChange={(e) => togglePack(o.id, e.target.checked)}
                  />
                  <span className="min-w-0 truncate">{o.label}</span>
                </label>
              ))}
            </div>
          )}
        </div>
        <Field label="Suggested agents" hint="owner/provider (or core/owner/provider), one per line — offered after creation, never authorized">
          <Textarea rows={3} value={agents} onChange={(e) => setAgents(e.target.value)} placeholder="jack/codex" className="font-mono text-xs" />
        </Field>
        {schedules.length > 0 && (
          <div className="flex flex-col gap-2">
            <span className="text-xs font-medium uppercase tracking-wide text-[var(--color-fg-muted)]">
              Schedules (created paused in new rooms)
            </span>
            <ul className="flex flex-col divide-y divide-[var(--color-border)] rounded-[var(--radius)] border border-[var(--color-border)]">
              {schedules.map((s, i) => (
                <li key={i} className="flex items-center gap-3 px-3 py-2 text-sm">
                  <div className="min-w-0 flex-1">
                    <p className="truncate">{s.text}</p>
                    <p className="truncate text-xs text-[var(--color-fg-subtle)]">
                      {describeRecurrence(s.recurrence, s.timezone)} · {s.timezone} · to <ScheduleRecipientName recipient={s.recipient} />
                      {s.channel ? ` · #${s.channel}` : ""}
                    </p>
                  </div>
                  <button
                    type="button"
                    onClick={() => setSchedules((list) => list.filter((_, j) => j !== i))}
                    title="Remove schedule"
                    aria-label="Remove schedule"
                    className="tactile text-[var(--color-fg-subtle)] hover:text-[var(--color-danger)]"
                  >
                    <TrashIcon size={15} />
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </Dialog>
  );
}

// InstantiateTemplateDialog creates a room from a template. With a template
// given it confirms the room id; without one (the "New room → from template"
// entry) it first offers a picker of the templates the member can see.
export function InstantiateTemplateDialog({
  open,
  template,
  onClose,
  onCreated,
}: {
  readonly open: boolean;
  readonly template?: RoomTemplate | null;
  readonly onClose: () => void;
  readonly onCreated: (roomId: string) => void;
}) {
  const toast = useToast();
  const { reauth } = useAuth();
  const [templates, setTemplates] = useState<RoomTemplate[] | null>(null);
  const [pickedId, setPickedId] = useState("");
  const [roomId, setRoomId] = useState("");
  const [vis, setVis] = useState<"private" | "shared">("private");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (!open) return;
    setRoomId("");
    setVis("private");
    setErr("");
    setPickedId(template?.templateId ?? "");
    if (template) return;
    let live = true;
    setTemplates(null);
    roomTemplatesApi
      .list()
      .then((r) => {
        if (!live) return;
        setTemplates(r.templates ?? []);
        setPickedId(r.templates?.[0]?.templateId ?? "");
      })
      .catch(() => {
        if (!live) return;
        setTemplates([]);
        toast.error("Could not load templates");
      });
    return () => {
      live = false;
    };
  }, [open, template, toast]);

  const chosen = template ?? templates?.find((t) => t.templateId === pickedId) ?? null;

  const submit = async () => {
    if (!chosen || busy) return;
    const problem = roomIdProblem(roomId);
    if (problem) {
      setErr(problem);
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const id = roomId.trim();
      const inst = await roomTemplatesApi.instantiate(chosen.templateId, { roomId: id, name: id, visibility: vis });
      rememberTemplateStart(inst);
      await reauth().catch(() => {});
      if (inst.failedMembers?.length) toast.info(`Could not add ${inst.failedMembers.join(", ")} — invite them from Members`);
      toast.success(`Room “${id}” created from “${chosen.name}”`);
      onCreated(id);
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setErr("A room with this id already exists");
      else {
        const msg = errorText(e, "create failed");
        setErr(msg);
        toast.error(`Could not create room: ${msg}`);
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="New room from template"
      description="You own the new room. Channels, prompt packs, members and paused schedules are set up from the template."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || !chosen}>
            {busy ? "Creating…" : "Create room"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {!template && (
          <Field label="Template">
            {templates === null ? (
              <p className="text-sm text-[var(--color-fg-subtle)]">Loading templates…</p>
            ) : templates.length === 0 ? (
              <p className="text-sm text-[var(--color-fg-muted)]">
                No templates yet. Save a room as a template from its header, or create one in the Library.
              </p>
            ) : (
              <Select className={selectClass} value={pickedId} onChange={(e) => setPickedId(e.target.value)}>
                {templates.map((t) => (
                  <option key={t.templateId} value={t.templateId}>
                    {t.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
        {chosen && (
          <p className="text-sm text-[var(--color-fg-muted)]">
            <span className="font-medium text-[var(--color-fg)]">{chosen.name}</span> · {templateSummary(chosen.body)}
          </p>
        )}
        <Field label="Room id" error={err || undefined}>
          <Input
            autoFocus
            placeholder="my-team-room"
            value={roomId}
            onChange={(e) => {
              setRoomId(e.target.value);
              if (err) setErr("");
            }}
            onKeyDown={(e) => isSubmitEnter(e) && void submit()}
          />
        </Field>
        <Field label="Visibility">
          <Segment
            value={vis}
            onChange={setVis}
            options={[
              { value: "private", label: "Private", hint: "invite only" },
              { value: "shared", label: "Shared", hint: "anyone joins" },
            ]}
          />
        </Field>
      </div>
    </Dialog>
  );
}

function ScheduleRecipientName({ recipient }: { readonly recipient: Parameters<typeof recipientLabel>[0] }) {
  const nameOf = useDisplayNames([recipient.kind === "agent" ? recipient.ownerId : ""]);
  return <>{recipientLabel(recipient, nameOf)}</>;
}
