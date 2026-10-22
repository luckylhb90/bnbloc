import { useState } from "react";
import { roomLinkAudience } from "@/lib/roomAccess";
import { XIcon } from "@phosphor-icons/react";
import { api, ApiError } from "@/lib/api";
import { runJSONAssist, useAssistTask } from "@/lib/assist";
import { PLAN_ROOM, roomPlanFrom } from "@/lib/assistDrafts";
import { useAuth } from "@/lib/auth";
import { contentLabel, type ContentComponent } from "@/lib/contentComponents";
import { contentApi } from "@/lib/contentComponentsApi";
import { errorText } from "@/lib/errors";
import { isSubmitEnter } from "@/lib/keys";
import { roomIdProblem } from "@/lib/roomId";
import { composerDrafts } from "@/lib/team";
import { Button } from "@/ui/Button";
import { Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { AssistButton, type AssistRunState } from "@/views/AssistButton";

// CreateRoomDialog is Plaza's "New room": an id, a name, a description and
// the visibility. "Plan with AI" (docs/prd/2026-10-13-ai-assist.md §6)
// turns what the room is for into a proposal (plan-room, from the member's
// library): the id, name and description, an expert and skill packs, and
// a first message. The member edits it; on Create the room is made, the
// expert and skill packs are turned on (one the member may not use is
// reported, not fatal) and the first message waits in the room's
// composer — never sent.

export function CreateRoomDialog({
  open,
  onClose,
  onCreated,
  onFromComponent,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (id: string) => void;
  onFromComponent?: () => void;
}) {
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [desc, setDesc] = useState("");
  const [vis, setVis] = useState<"private" | "shared">("private");
  // null: not chosen, so the visibility's default holds (a shared room is
  // readable before joining, a private one is not: store.DefaultPreviewable).
  const [previewable, setPreviewable] = useState<"on" | "off" | null>(null);
  const [goal, setGoal] = useState("");
  const [planning, setPlanning] = useState<AssistRunState>("idle");
  const [expert, setExpert] = useState<ContentComponent | null>(null);
  const [skills, setSkills] = useState<readonly ContentComponent[]>([]);
  const [firstMessage, setFirstMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const { reauth } = useAuth();
  const toast = useToast();
  const planTask = useAssistTask(PLAN_ROOM);

  const reset = () => {
    setId("");
    setName("");
    setDesc("");
    setVis("private");
    setPreviewable(null);
    setGoal("");
    setExpert(null);
    setSkills([]);
    setFirstMessage("");
    setErr("");
  };

  const plan = async () => {
    if (!planTask || planning !== "idle" || !goal.trim()) return;
    setPlanning("waiting");
    try {
      const [reply, experts, packs] = await Promise.all([
        runJSONAssist(planTask.name, { text: goal.trim() }),
        contentApi.list("expert").then((r) => r.components ?? []),
        contentApi.list("skill").then((r) => r.components ?? []),
      ]);
      const p = roomPlanFrom(reply.fields, experts, packs);
      if (p.roomId) setId(p.roomId);
      if (p.name) setName(p.name);
      if (p.description) setDesc(p.description);
      setExpert(p.expert);
      setSkills(p.skills);
      setFirstMessage(p.firstMessage);
      if (err) setErr("");
    } catch (e) {
      toast.error(errorText(e, "Could not plan the room"));
    } finally {
      setPlanning("idle");
    }
  };

  // setUp turns on the planned expert and skill packs; what fails (one the
  // member may not use) is named, the room stays.
  const setUp = async (roomId: string) => {
    const left: string[] = [];
    if (expert) await contentApi.putUse("room-expert", expert.id, { roomId }).catch(() => left.push(contentLabel(expert)));
    for (const s of skills) await contentApi.putUse("room", s.id, { roomId }).catch(() => left.push(contentLabel(s)));
    if (left.length > 0) toast.error(`Could not turn on ${left.join(", ")} in the room`);
    if (firstMessage.trim()) composerDrafts.queue(roomId, firstMessage.trim());
  };

  const submit = async () => {
    const problem = roomIdProblem(id);
    if (problem) {
      setErr(problem);
      return;
    }
    setBusy(true);
    setErr("");
    const roomId = id.trim();
    try {
      await api.createRoom({
        roomId,
        name: name.trim() || roomId,
        description: desc.trim(),
        visibility: vis,
        // Untouched, the server's default for the visibility applies.
        ...(previewable === null ? {} : { previewable: previewable === "on" }),
      });
      // Refresh room-scope so the new room is immediately reachable.
      await reauth().catch(() => {});
      await setUp(roomId);
      reset();
      onCreated(roomId);
    } catch (e) {
      // The form stays open with the id so the member can adjust it.
      if (e instanceof ApiError && e.status === 409) {
        setErr("A room with this id already exists");
      } else {
        const msg = errorText(e, "create failed");
        setErr(msg);
        toast.error(`Could not create room: ${msg}`);
      }
    } finally {
      setBusy(false);
    }
  };

  const close = () => {
    reset();
    onClose();
  };
  return (
    <Dialog
      open={open}
      onClose={close}
      title="New room"
      description="Rooms are private by default. You can change this anytime."
      footer={
        <>
          {onFromComponent && (
            <Button
              variant="outline"
              className="mr-auto"
              onClick={() => {
                reset();
                onFromComponent();
              }}
            >
              From a room component…
            </Button>
          )}
          <Button variant="ghost" onClick={close}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} disabled={busy || planning !== "idle"}>
            {busy ? "Creating…" : "Create room"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {planTask && (
          <div className="flex flex-col gap-2 rounded-[var(--radius)] border border-dashed border-[var(--color-border)] bg-[var(--color-surface-2)]/40 p-2">
            <Textarea
              rows={2}
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder="What is the room for? e.g. prepare the Q3 launch review with the product and design teams"
              aria-label="What the room is for"
              disabled={planning !== "idle"}
            />
            <AssistButton variant="text" label="Plan with AI" state={planning} onRun={() => void plan()} disabled={!goal.trim()} className="self-end" />
          </div>
        )}
        <Field label="Room id" error={err}>
          <Input
            autoFocus
            placeholder="my-team-room"
            value={id}
            onChange={(e) => {
              setId(e.target.value);
              if (err) setErr("");
            }}
            onKeyDown={(e) => isSubmitEnter(e) && void submit()}
          />
        </Field>
        <Field label="Name" hint="Optional — shown instead of the id">
          <Input placeholder="My team room" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Description" hint="Optional — what is this room for?">
          <Input placeholder="Design reviews and agent pairing" value={desc} onChange={(e) => setDesc(e.target.value)} />
        </Field>
        {(expert || skills.length > 0) && (
          <Field label="Turned on in the room" hint="From your library; remove any you don't want">
            <div className="flex flex-wrap gap-1.5">
              {expert && <PlanChip label={`Expert: ${contentLabel(expert)}`} onRemove={() => setExpert(null)} />}
              {skills.map((s) => (
                <PlanChip key={s.id} label={contentLabel(s)} onRemove={() => setSkills((cur) => cur.filter((x) => x.id !== s.id))} />
              ))}
            </div>
          </Field>
        )}
        {(firstMessage || (planning === "idle" && (expert || skills.length > 0))) && (
          <Field label="First message" hint="Waits in the room's composer; you send it">
            <Textarea rows={3} value={firstMessage} onChange={(e) => setFirstMessage(e.target.value)} />
          </Field>
        )}
        <Field label="Visibility">
          <Segment
            value={vis}
            onChange={setVis}
            options={[
              { value: "private", label: "Private", hint: "invite code" },
              { value: "shared", label: "Shared", hint: "anyone joins" },
            ]}
          />
        </Field>
        <Field label="Readable before joining">
          <Segment
            value={previewable ?? (vis === "shared" ? "on" : "off")}
            onChange={setPreviewable}
            options={[
              { value: "on", label: "On", hint: "read-only for your organization" },
              { value: "off", label: "Off", hint: "members only" },
            ]}
          />
        </Field>
        <p className="-mt-2 text-xs text-[var(--color-fg-subtle)]">{roomLinkAudience(vis, (previewable ?? (vis === "shared" ? "on" : "off")) === "on")}</p>
      </div>
    </Dialog>
  );
}

function PlanChip({ label, onRemove }: { readonly label: string; readonly onRemove: () => void }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-full border border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] px-2.5 py-0.5 text-xs text-[var(--color-accent)]">
      {label}
      <button type="button" onClick={onRemove} aria-label={`Remove ${label}`} title="Remove" className="tactile hover:text-[var(--color-fg)]">
        <XIcon size={11} />
      </button>
    </span>
  );
}
