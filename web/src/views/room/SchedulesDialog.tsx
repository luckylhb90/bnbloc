import { channelsApi } from "@/lib/channelsApi";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Select } from "@/ui/Select";
import {
  ClockIcon,
  PauseIcon,
  PencilSimpleIcon,
  PlayIcon,
  PaperPlaneRightIcon,
  PlusIcon,
  SparkleIcon,
  TrashIcon,
  WarningIcon,
} from "@phosphor-icons/react";
import { ApiError, type Channel } from "@/lib/api";
import { cn } from "@/lib/cn";
import { sameUser } from "@/lib/format";
import {
  WEEKDAYS,
  browserTimezone,
  describeRecurrence,
  formFromRecurrence,
  formatInZone,
  isBorrowedRecipient,
  noticeToOf,
  noticeWhereOf,
  recipientLabel,
  recurrenceFromForm,
  scheduleStatus,
  timezoneOptions,
  waitsForAgent,
  type RecurrenceForm,
  type NoticeTarget,
  type RecurrenceKind,
  type RoomSchedule,
  type ScheduleInput,
  type ScheduleRecipient,
} from "@/lib/schedules";
import { roomGroupsApi } from "@/lib/roomGroupsApi";
import { schedulesApi } from "@/lib/schedulesApi";
import type { ScheduleRunReply } from "@/lib/wire.gen";
import { Button } from "@/ui/Button";
import { Badge } from "@/ui/Card";
import { ConfirmDialog, Dialog } from "@/ui/Dialog";
import { Field, Input, Textarea } from "@/ui/Input";
import { Segment, Switch } from "@/ui/Segment";
import { useToast } from "@/ui/Toast";
import { agentRows, type PickerTarget } from "@/views/composer/agentRows";
import type { RoomAgent } from "@/views/composer/types";
import { useDisplayNames } from "@/lib/useDisplayName";
import { UserName } from "@/views/UserName";
import { errorText } from "@/lib/errors";
import { agentIdentity } from "@/lib/roomAgents";
import { runJSONAssist, useAssistTask } from "@/lib/assist";
import { DRAFT_SCHEDULE, scheduleDraftFrom } from "@/lib/assistDrafts";
import { isSubmitEnter } from "@/lib/keys";
import { AssistButton, type AssistRunState } from "@/views/AssistButton";
import { ScheduleEventFields } from "@/views/room/ScheduleEventFields";
import { ScheduleHookRow } from "@/views/room/ScheduleHookRow";

const SELECT =
  "h-9 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm text-[var(--color-fg)] focus:outline-none focus:border-[var(--color-accent)]";

const KINDS = [
  { value: "once" as const, label: "Once" },
  { value: "daily" as const, label: "Daily" },
  { value: "weekly" as const, label: "Weekly" },
  { value: "cron" as const, label: "Custom" },
  { value: "event" as const, label: "When something happens" },
];

// SchedulesDialog is the room's scheduled prompts panel (N2): every member
// sees the room's schedules; the creator edits, runs now, pauses/resumes and
// deletes; the room owner or an admin may pause or delete any schedule.
export function SchedulesDialog({
  roomId,
  subject,
  moderator,
  roomAgents,
  localTargets,
  aiTask,
  editId,
  onClose,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  // The caller may pause or delete anyone's schedule (canInvite).
  readonly moderator: boolean;
  readonly roomAgents: RoomAgent[];
  readonly localTargets: readonly PickerTarget[];
  // Opens straight into a new AI task starting with this text (an app's
  // "Automate…").
  readonly aiTask?: string;
  // Opens straight into this schedule's editor (the room's "pick an agent
  // for" list).
  readonly editId?: string;
  readonly onClose: () => void;
}) {
  const toast = useToast();
  const [list, setList] = useState<RoomSchedule[] | null>(null);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [editing, setEditing] = useState<RoomSchedule | "new" | null>(aiTask !== undefined ? "new" : null);
  const [deleting, setDeleting] = useState<RoomSchedule | null>(null);
  const [trying, setTrying] = useState<RoomSchedule | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const nameOf = useDisplayNames((list ?? []).flatMap((s) => [s.createdBy, s.recipient.kind === "agent" ? s.recipient.ownerId : ""]));

  const reload = useCallback(() => {
    schedulesApi
      .list(roomId)
      .then((r) => setList(r.schedules ?? []))
      .catch((e) => {
        setList([]);
        toast.error(errorText(e, "Could not load schedules"));
      });
  }, [roomId, toast]);

  useEffect(() => {
    reload();
    channelsApi
      .list(roomId)
      .then((r) => setChannels(r.channels ?? []))
      .catch(() => setChannels([]));
  }, [roomId, reload]);

  // The schedule the dialog was opened for, once the list has it.
  const [opened, setOpened] = useState(false);
  useEffect(() => {
    if (opened || !editId || !list) return;
    setOpened(true);
    const s = list.find((x) => x.scheduleId === editId);
    if (s) setEditing(s);
  }, [editId, list, opened]);

  const channelName = (id?: string) => (id ? channels.find((c) => c.channelId === id)?.name : undefined);

  const act = async (s: RoomSchedule, label: string, fn: () => Promise<unknown>, done: string) => {
    setBusy(s.scheduleId + label);
    try {
      // A run of an AI task or a notice answers what it did; say that, not a
      // fixed "done" (a failed run is a reply, not a rejected call).
      const answer = await fn();
      const run = label === "run" ? (answer as Partial<ScheduleRunReply> | undefined) : undefined;
      if (run?.status === "error") toast.error(run.error || "The run failed");
      else toast.success(run?.summary || done);
      reload();
    } catch (e) {
      toast.error(errorText(e, `Could not ${label} the schedule`));
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <Dialog
        open
        onClose={onClose}
        size="lg"
        title="Schedules"
        description="Messages posted into this room at set times, and AI tasks carried out at them, as the member who scheduled them."
        footer={
          <>
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
            <Button variant="primary" onClick={() => setEditing("new")}>
              <PlusIcon size={14} /> New schedule
            </Button>
          </>
        }
      >
        {list === null ? (
          <p className="py-6 text-center text-sm text-[var(--color-fg-muted)]">Loading…</p>
        ) : list.length === 0 ? (
          <div className="flex flex-col items-center gap-2 py-8 text-center">
            <ClockIcon size={26} className="text-[var(--color-fg-subtle)]" />
            <p className="text-sm font-medium">No schedules yet</p>
            <p className="max-w-sm text-xs text-[var(--color-fg-muted)]">
              Schedule a recurring prompt — a daily standup question to an agent — a notice to the room, a group chat or people, or a task for AI in plain words.
            </p>
          </div>
        ) : (
          <ul className="flex flex-col gap-2">
            {list.map((s) => {
              const mine = sameUser(s.createdBy, subject);
              const waiting = waitsForAgent(s.recipient);
              const status = scheduleStatus(s);
              const ch = channelName(s.channelId);
              return (
                <li key={s.scheduleId} className="rounded-[var(--radius)] border border-[var(--color-border)] p-3">
                  <div className="flex items-start gap-2">
                    <div className="min-w-0 flex-1">
                      {s.recipient.kind === "notice" && s.recipient.title && <p className="break-words text-sm font-medium">{s.recipient.title}</p>}
                      <p className="line-clamp-2 whitespace-pre-wrap break-words text-sm">{s.text}</p>
                      <p className="mt-1 text-xs text-[var(--color-fg-muted)]">
                        To <span className="font-medium text-[var(--color-fg)]">{recipientLabel(s.recipient, nameOf)}</span>
                        {ch ? ` in #${ch}` : ""} · {describeRecurrence(s.recurrence, s.timezone)} ({s.timezone})
                      </p>
                      <p className="mt-0.5 text-xs text-[var(--color-fg-subtle)]">
                        {mine ? "You" : nameOf(s.createdBy)}
                        {s.enabled && s.nextRunAtMs ? ` · next ${formatInZone(s.nextRunAtMs, s.timezone)}` : ""}
                        {s.lastRunAtMs ? ` · last ${formatInZone(s.lastRunAtMs, s.timezone)} (${s.lastStatus ?? "?"})` : ""}
                      </p>
                      {s.lastSummary && <p className="mt-0.5 text-xs text-[var(--color-fg-muted)]">Last run: {s.lastSummary}</p>}
                    </div>
                    <Badge tone={status.tone === "ok" ? "accent" : status.tone === "warn" ? "warning" : "neutral"} title={status.label}>
                      <span className="max-w-[12rem] truncate">{status.label}</span>
                    </Badge>
                  </div>
                  <div className="mt-2 flex flex-wrap items-center justify-end gap-1">
                    {s.enabled && (mine || moderator) && (
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy !== null}
                        onClick={() => void act(s, "pause", () => schedulesApi.update(roomId, s.scheduleId, { enabled: false }), "Schedule paused")}
                      >
                        <PauseIcon size={14} /> Pause
                      </Button>
                    )}
                    {waiting && mine && (
                      <Button size="sm" variant="outline" onClick={() => setEditing(s)}>
                        <PencilSimpleIcon size={14} /> Pick an agent
                      </Button>
                    )}
                    {!s.enabled && mine && !waiting && (
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy !== null}
                        onClick={() => void act(s, "resume", () => schedulesApi.update(roomId, s.scheduleId, { enabled: true }), "Schedule resumed")}
                      >
                        <PlayIcon size={14} /> Resume
                      </Button>
                    )}
                    {mine && !waiting && (
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy !== null}
                        onClick={() => void act(s, "run", () => schedulesApi.runNow(roomId, s.scheduleId), s.recipient.kind === "ai" ? "Ran now" : s.recipient.kind === "notice" ? "Sent now" : "Posted now")}
                      >
                        <PaperPlaneRightIcon size={14} /> Run now
                      </Button>
                    )}
                    {mine && (s.recipient.kind === "ai" || s.recipient.kind === "notice") && (
                      <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => setTrying(s)}>
                        <SparkleIcon size={14} /> Try it
                      </Button>
                    )}
                    {mine && (
                      <Button size="sm" variant="ghost" onClick={() => setEditing(s)}>
                        <PencilSimpleIcon size={14} /> Edit
                      </Button>
                    )}
                    {(mine || moderator) && (
                      <Button size="sm" variant="ghost" onClick={() => setDeleting(s)} aria-label="Delete schedule" title="Delete schedule">
                        <TrashIcon size={14} />
                      </Button>
                    )}
                  </div>
                  {s.recurrence.kind === "event" && s.recurrence.source === "webhook" && (
                    <ScheduleHookRow roomId={roomId} schedule={s} mine={mine} moderator={moderator} onChanged={reload} />
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </Dialog>
      {editing && (
        <ScheduleEditor
          roomId={roomId}
          subject={subject}
          schedule={editing === "new" ? null : editing}
          seed={editing === "new" && aiTask !== undefined ? { text: aiTask, recipient: { kind: "ai" } } : undefined}
          channels={channels}
          roomAgents={roomAgents}
          localTargets={localTargets}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reload();
          }}
        />
      )}
      {trying && <TryRunDialog roomId={roomId} schedule={trying} onClose={() => setTrying(null)} />}
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        onConfirm={() => {
          const s = deleting;
          setDeleting(null);
          if (s) void act(s, "delete", () => schedulesApi.remove(roomId, s.scheduleId), "Schedule deleted");
        }}
        title="Delete this schedule?"
        description="Nothing more will be posted by it. Messages it already posted stay in the room."
        confirmLabel="Delete"
        danger
      />
    </>
  );
}

// recipientKey identifies a recipient option ("room", "pick" for an agent
// not picked yet, or the agent identity).
function recipientKey(r: ScheduleRecipient): string {
  if (r.kind === "room" || r.kind === "ai" || r.kind === "notice") return r.kind;
  if (waitsForAgent(r)) return "pick";
  return agentIdentity({ ownerId: r.ownerId ?? "", core: r.core || undefined, provider: r.provider ?? "" });
}

// ScheduleEditor creates or edits one schedule. The recipient choices are the
// composer's agent rows (agentRows: my local targets + the room's advertised
// agents, grouped by core), plus the people-only room.
function ScheduleEditor({
  roomId,
  subject,
  schedule,
  seed,
  channels,
  roomAgents,
  localTargets,
  onClose,
  onSaved,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly schedule: RoomSchedule | null;
  // A new schedule's starting text and recipient.
  readonly seed?: { readonly text: string; readonly recipient: ScheduleRecipient };
  readonly channels: Channel[];
  readonly roomAgents: RoomAgent[];
  readonly localTargets: readonly PickerTarget[];
  readonly onClose: () => void;
  readonly onSaved: () => void;
}) {
  const toast = useToast();
  const me = subject ?? "";
  const nameOf = useDisplayNames([...roomAgents.map((a) => a.ownerId), schedule?.recipient.kind === "agent" ? schedule.recipient.ownerId : ""]);
  const [text, setText] = useState(schedule?.text ?? seed?.text ?? "");
  const [timezone, setTimezone] = useState(schedule?.timezone ?? browserTimezone());
  const [channelId, setChannelId] = useState(schedule?.channelId ?? "");
  const [recipient, setRecipient] = useState<ScheduleRecipient>(schedule?.recipient ?? seed?.recipient ?? { kind: "room" });
  // A notice's destination, title and whether an AI writes its body (kind "notice").
  const [notice, setNotice] = useState(() => ({
    where: noticeWhereOf(schedule?.recipient.to),
    title: schedule?.recipient.title ?? "",
    compose: schedule?.recipient.compose ?? false,
  }));
  const [groupNames, setGroupNames] = useState<string[]>([]);
  useEffect(() => {
    let live = true;
    roomGroupsApi
      .list(roomId)
      .then((r) => live && setGroupNames((r.groups ?? []).map((g) => g.name)))
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [roomId]);
  const [form, setForm] = useState<RecurrenceForm>(() => formFromRecurrence(schedule?.recurrence, schedule?.timezone ?? browserTimezone()));
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const zones = useMemo(() => timezoneOptions(timezone), [timezone]);

  // Recipient options: the composer's agent rows mapped to schedule
  // recipients; a stored recipient no longer advertised stays selectable.
  const options = useMemo(() => {
    const out: { key: string; group: string; label: string; recipient: ScheduleRecipient }[] = [];
    for (const g of agentRows(localTargets, roomAgents, subject)) {
      for (const row of g.rows) {
        const r: ScheduleRecipient = { kind: "agent", ownerId: row.ownerId, provider: row.provider, ...(row.core ? { core: row.core } : {}) };
        out.push({ key: recipientKey(r), group: g.group, label: `${row.provider} — ${row.self ? "yours" : nameOf(row.ownerId)}`, recipient: r });
      }
    }
    if (recipient.kind === "agent" && !waitsForAgent(recipient) && !out.some((o) => o.key === recipientKey(recipient))) {
      out.push({ key: recipientKey(recipient), group: "not currently online", label: recipientLabel(recipient, nameOf), recipient });
    }
    return out;
  }, [localTargets, roomAgents, subject, recipient, nameOf]);
  const groups = useMemo(() => [...new Set(options.map((o) => o.group))], [options]);
  const borrowed = isBorrowedRecipient(recipient, me);
  const setKind = (kind: RecurrenceKind) => setForm((f) => ({ ...f, kind }));
  // "Describe it" (AI assist PRD §6): plain words to the message and the
  // recurrence, in the form's timezone; the member reviews and saves.
  const describeTask = useAssistTask(DRAFT_SCHEDULE);
  const [said, setSaid] = useState("");
  const [describing, setDescribing] = useState<AssistRunState>("idle");
  const describe = async () => {
    if (!describeTask || describing !== "idle" || !said.trim()) return;
    setDescribing("waiting");
    try {
      const r = await runJSONAssist(describeTask.name, { text: said.trim(), roomId, timezone });
      const d = scheduleDraftFrom(r.fields);
      if (d.text) setText(seed && recipient.kind === "ai" && !d.text.includes(seed.text.trim()) ? seed.text + d.text : d.text);
      setForm((f) => ({ ...f, ...d.form }));
      if (!d.form.kind) toast.error("Could not read when to post it; set the time below");
      setErr("");
    } catch (e) {
      toast.error(errorText(e, "Could not read the schedule"));
    } finally {
      setDescribing("idle");
    }
  };
  const preview = useMemo(() => {
    const built = recurrenceFromForm(form, timezone);
    return built.recurrence ? describeRecurrence(built.recurrence, timezone) : null;
  }, [form, timezone]);

  const isNotice = recipient.kind === "notice";
  const save = async () => {
    if (isNotice) {
      if (noticeToOf(notice.where) === undefined) {
        setErr(notice.where.target === "group" ? "Pick the group chat" : "Name who gets it");
        return;
      }
      if (!notice.title.trim()) {
        setErr("Write the notice's title");
        return;
      }
      if (notice.compose && !text.trim()) {
        setErr("Say what the AI should write");
        return;
      }
    } else if (!text.trim()) {
      setErr(recipient.kind === "ai" ? "Write the task" : "Write the message to post");
      return;
    } else if (waitsForAgent(recipient)) {
      setErr("Pick one of the agents to send it to");
      return;
    }
    const built = recurrenceFromForm(form, timezone);
    if (!built.recurrence) {
      setErr(built.error ?? "Invalid schedule");
      return;
    }
    setErr("");
    setBusy(true);
    const sent: ScheduleRecipient = isNotice
      ? { kind: "notice", to: noticeToOf(notice.where), title: notice.title.trim(), ...(notice.compose ? { compose: true } : {}) }
      : recipient;
    const body: ScheduleInput = { text, recipient: sent, recurrence: built.recurrence, timezone, channelId };
    try {
      if (schedule) {
        await schedulesApi.update(roomId, schedule.scheduleId, body);
        toast.success(waitsForAgent(schedule.recipient) ? "Agent picked: turn the schedule on with Resume" : "Schedule saved");
      } else {
        await schedulesApi.create(roomId, { ...body, enabled });
        toast.success(enabled ? "Schedule created" : "Schedule created (paused)");
      }
      onSaved();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        toast.error(e.message || "Schedule limit reached");
      } else if (e instanceof ApiError && (e.status === 400 || e.status === 404 || e.status === 413)) {
        setErr(e.message || "The schedule is not valid");
      } else {
        toast.error(errorText(e, "Could not save the schedule"));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      size="lg"
      title={schedule ? "Edit schedule" : "New schedule"}
      description={
        isNotice
          ? "A notice sent at each run, as you, through your organization's notifications."
          : recipient.kind === "ai"
            ? "AI carries the task out at each run, with your permissions."
            : "Posted as you, through the normal chat path."
      }
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy}>
            {busy ? "Saving…" : schedule ? "Save" : "Create schedule"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {describeTask && (
          <div className="flex items-center gap-2 rounded-[var(--radius)] border border-dashed border-[var(--color-border)] bg-[var(--color-surface-2)]/40 p-2">
            <Input
              className="min-w-0 flex-1"
              value={said}
              onChange={(e) => setSaid(e.target.value)}
              onKeyDown={(e) => isSubmitEnter(e) && void describe()}
              placeholder="Describe it, e.g. every weekday at 9:30 ask the agent for yesterday's open issues"
              aria-label="Describe the schedule"
              disabled={describing !== "idle"}
            />
            <AssistButton variant="text" label="Describe it" state={describing} onRun={() => void describe()} disabled={!said.trim()} />
          </div>
        )}
        {isNotice && (
          <>
            <Field label="Title">
              <Input value={notice.title} onChange={(e) => setNotice((n) => ({ ...n, title: e.target.value }))} placeholder="Weekly report {week}" />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Goes to">
                <Select
                  className={SELECT}
                  value={notice.where.target}
                  onChange={(e) => setNotice((n) => ({ ...n, where: { ...n.where, target: e.target.value as NoticeTarget } }))}
                >
                  <option value="room">The room's members</option>
                  <option value="group">A group chat of this room</option>
                  <option value="logins">Specific people</option>
                </Select>
              </Field>
              {notice.where.target === "group" && (
                <Field label="Group chat" hint={groupNames.length === 0 ? "None is connected to this room yet (the room's Group chats)." : undefined}>
                  {groupNames.length > 0 ? (
                    <Select className={SELECT} value={notice.where.group} onChange={(e) => setNotice((n) => ({ ...n, where: { ...n.where, group: e.target.value } }))}>
                      <option value="">Choose…</option>
                      {[...new Set([...groupNames, notice.where.group].filter((g) => g !== ""))].map((g) => (
                        <option key={g} value={g}>
                          {g}
                        </option>
                      ))}
                    </Select>
                  ) : (
                    <Input value={notice.where.group} onChange={(e) => setNotice((n) => ({ ...n, where: { ...n.where, group: e.target.value } }))} placeholder="Group name" />
                  )}
                </Field>
              )}
              {notice.where.target === "logins" && (
                <Field label="People" hint="Logins, separated by commas">
                  <Input value={notice.where.logins} onChange={(e) => setNotice((n) => ({ ...n, where: { ...n.where, logins: e.target.value } }))} placeholder="maya@example.com, jack@example.com" />
                </Field>
              )}
            </div>
            <Switch checked={notice.compose} onChange={(compose) => setNotice((n) => ({ ...n, compose }))} label="Let AI write the message each time" />
          </>
        )}
        <Field
          label={isNotice ? (notice.compose ? "What the AI should write" : "Message") : recipient.kind === "ai" ? "Task" : "Message"}
          hint={
            isNotice
              ? notice.compose
                ? "In your own words. At each run AI writes the message from this; the title and who gets it stay as set."
                : "Sent as written. {date} {time} {week} {month} {year} {day} and {room} stand for the run's own values; write {{ for a brace."
              : recipient.kind === "ai"
                ? "In your own words. At each run AI reads the room's apps' data and can notify people or a connected group chat (group:<name>), post here or ask an agent, as you. Try it before turning it on."
                : undefined
          }
        >
          <Textarea
            autoFocus
            rows={4}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={
              isNotice
                ? notice.compose
                  ? "Ask the team, friendly and short, to fill in this week's report."
                  : "Please fill in this week's report ({week}) by Friday."
                : recipient.kind === "ai"
                  ? "Remind whoever has not filled in this week's report, with the link."
                  : "What changed since yesterday? Summarize open questions."
            }
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Send to">
            <Select
              className={SELECT}
              value={recipientKey(recipient)}
              onChange={(e) => {
                const o = options.find((x) => x.key === e.target.value);
                setRecipient(o ? o.recipient : { kind: e.target.value === "ai" ? "ai" : e.target.value === "notice" ? "notice" : "room" });
              }}
            >
              {waitsForAgent(recipient) && (
                <option value="pick" disabled>
                  Choose an agent…
                </option>
              )}
              <option value="room">The room (people only)</option>
              <option value="notice">Notice: send a message to the room, a group chat or people</option>
              <option value="ai">AI: carry out the task</option>
              {groups.map((g) => (
                <optgroup key={g} label={g}>
                  {options
                    .filter((o) => o.group === g)
                    .map((o) => (
                      <option key={o.key} value={o.key}>
                        {o.label}
                      </option>
                    ))}
                </optgroup>
              ))}
            </Select>
          </Field>
          <Field label="Channel">
            <Select className={SELECT} value={channelId} onChange={(e) => setChannelId(e.target.value)}>
              <option value="">Default channel</option>
              {channels
                .filter((c) => !c.isDefault)
                .map((c) => (
                  <option key={c.channelId} value={c.channelId}>
                    #{c.name}
                  </option>
                ))}
            </Select>
          </Field>
        </div>
        {borrowed && recipient.kind === "agent" && (
          <p className="flex items-start gap-2 rounded-[var(--radius)] border border-[var(--color-warning)]/30 bg-[var(--color-warning)]/10 px-3 py-2 text-xs text-[var(--color-fg-muted)]">
            <WarningIcon size={14} className="mt-0.5 shrink-0 text-[var(--color-warning)]" />
            <span>
              This posts to another member's agent. It runs only while <UserName id={recipient.ownerId ?? ""} /> has authorized you in this room;
              otherwise the run is refused at drive time.
            </span>
          </p>
        )}
        <Field label="When" hint={preview ? `${preview} (${timezone})` : undefined}>
          <Segment options={KINDS} value={form.kind} onChange={setKind} />
        </Field>
        {form.kind === "once" && (
          <Field label="Date and time" hint={`In ${timezone}`}>
            <Input type="datetime-local" value={form.onceLocal} onChange={(e) => setForm((f) => ({ ...f, onceLocal: e.target.value }))} />
          </Field>
        )}
        {(form.kind === "daily" || form.kind === "weekly") && (
          <Field label="Time" hint={`In ${timezone}`}>
            <Input type="time" value={form.time} onChange={(e) => setForm((f) => ({ ...f, time: e.target.value }))} className="max-w-[10rem]" />
          </Field>
        )}
        {form.kind === "weekly" && (
          <Field label="Days">
            <div className="flex flex-wrap gap-1.5">
              {WEEKDAYS.map((label, d) => {
                const on = form.days.includes(d);
                return (
                  <button
                    key={label}
                    type="button"
                    aria-pressed={on}
                    onClick={() => setForm((f) => ({ ...f, days: on ? f.days.filter((x) => x !== d) : [...f.days, d] }))}
                    className={cn(
                      "tactile rounded-full border px-2.5 py-0.5 text-xs",
                      on
                        ? "border-[var(--color-accent)]/40 bg-[var(--color-accent-soft)] text-[var(--color-accent)]"
                        : "border-[var(--color-border)] text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
                    )}
                  >
                    {label}
                  </button>
                );
              })}
            </div>
          </Field>
        )}
        {form.kind === "event" && <ScheduleEventFields form={form} onChange={setForm} />}
        {form.kind === "cron" && (
          <Field label="Cron expression" hint="minute hour day month weekday, at least 5 minutes apart">
            <Input value={form.cron} onChange={(e) => setForm((f) => ({ ...f, cron: e.target.value }))} placeholder="0 9 * * 1-5" className="font-mono" />
          </Field>
        )}
        <Field label="Timezone">
          <Select className={SELECT} value={timezone} onChange={(e) => setTimezone(e.target.value)}>
            {zones.map((z) => (
              <option key={z} value={z}>
                {z}
              </option>
            ))}
          </Select>
        </Field>
        {!schedule && <Switch checked={enabled} onChange={setEnabled} label="Start active" />}
        {err && <p className="text-sm text-[var(--color-danger)]">{err}</p>}
      </div>
    </Dialog>
  );
}

// TryRunDialog is an AI task's or a notice's dry run: what a run would do now (real
// reads, nothing sent), for its creator to check before it runs for real.
function TryRunDialog({ roomId, schedule, onClose }: { readonly roomId: string; readonly schedule: RoomSchedule; readonly onClose: () => void }) {
  const [reply, setReply] = useState<ScheduleRunReply | null>(null);
  const [failed, setFailed] = useState("");
  useEffect(() => {
    let live = true;
    schedulesApi
      .tryAI(roomId, schedule.scheduleId)
      .then((r) => live && setReply(r))
      .catch((e) => live && setFailed(errorText(e, "Could not try it")));
    return () => {
      live = false;
    };
  }, [roomId, schedule.scheduleId]);
  return (
    <Dialog
      open
      onClose={onClose}
      title={schedule.recipient.kind === "notice" ? "Try the notice" : "Try the task"}
      description="What a run would do now. It read for real and sent nothing."
      footer={
        <Button variant="primary" onClick={onClose}>
          Close
        </Button>
      }
    >
      {failed ? (
        <p className="text-sm text-[var(--color-danger)]">{failed}</p>
      ) : !reply ? (
        <p className="py-4 text-center text-sm text-[var(--color-fg-muted)]">Running it…</p>
      ) : (
        <div className="flex flex-col gap-3 text-sm">
          {reply.error ? <p className="text-[var(--color-danger)]">{reply.error}</p> : reply.summary && <p>{reply.summary}</p>}
          {reply.actions.length === 0 ? (
            <p className="text-xs text-[var(--color-fg-muted)]">It would do nothing.</p>
          ) : (
            <ul className="flex flex-col gap-1">
              {reply.actions.map((a, i) => (
                <li key={i} className="rounded-[var(--radius)] border border-[var(--color-border)] px-2 py-1 text-xs">
                  <span className="font-medium">{a.name}</span> <span className="text-[var(--color-fg-muted)]">{a.detail}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Dialog>
  );
}
