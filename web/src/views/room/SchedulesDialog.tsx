import { useCallback, useEffect, useMemo, useState } from "react";
import { Select } from "@/ui/Select";
import {
  ClockIcon,
  PauseIcon,
  PencilSimpleIcon,
  PlayIcon,
  PaperPlaneRightIcon,
  PlusIcon,
  TrashIcon,
  WarningIcon,
} from "@phosphor-icons/react";
import { api, ApiError, type Channel } from "@/lib/api";
import { cn } from "@/lib/cn";
import { sameUser } from "@/lib/format";
import {
  WEEKDAYS,
  browserTimezone,
  describeRecurrence,
  formFromRecurrence,
  formatInZone,
  isBorrowedRecipient,
  recipientLabel,
  recurrenceFromForm,
  scheduleStatus,
  timezoneOptions,
  type RecurrenceForm,
  type RecurrenceKind,
  type RoomSchedule,
  type ScheduleInput,
  type ScheduleRecipient,
} from "@/lib/schedules";
import { schedulesApi } from "@/lib/schedulesApi";
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

const SELECT =
  "h-9 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 text-sm text-[var(--color-fg)] focus:outline-none focus:border-[var(--color-accent)]";

const KINDS = [
  { value: "once" as const, label: "Once" },
  { value: "daily" as const, label: "Daily" },
  { value: "weekly" as const, label: "Weekly" },
  { value: "cron" as const, label: "Custom" },
];

function errorText(e: unknown, fallback: string): string {
  return e instanceof ApiError && e.message ? e.message : fallback;
}

// SchedulesDialog is the room's scheduled prompts panel (N2): every member
// sees the room's schedules; the creator edits, runs now, pauses/resumes and
// deletes; the room owner or an admin may pause or delete any schedule.
export function SchedulesDialog({
  roomId,
  subject,
  myRole,
  roomAgents,
  localTargets,
  onClose,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly myRole: string | null;
  readonly roomAgents: RoomAgent[];
  readonly localTargets: readonly PickerTarget[];
  readonly onClose: () => void;
}) {
  const toast = useToast();
  const [list, setList] = useState<RoomSchedule[] | null>(null);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [editing, setEditing] = useState<RoomSchedule | "new" | null>(null);
  const [deleting, setDeleting] = useState<RoomSchedule | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const moderator = myRole === "owner" || myRole === "admin";
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
    api
      .channels(roomId)
      .then((r) => setChannels(r.channels ?? []))
      .catch(() => setChannels([]));
  }, [roomId, reload]);

  const channelName = (id?: string) => (id ? channels.find((c) => c.channelId === id)?.name : undefined);

  const act = async (s: RoomSchedule, label: string, fn: () => Promise<unknown>, done: string) => {
    setBusy(s.scheduleId + label);
    try {
      await fn();
      toast.success(done);
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
        description="Messages posted into this room at set times, as the member who scheduled them."
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
              Schedule a recurring prompt — a daily standup question to an agent, a weekly reminder to the room.
            </p>
          </div>
        ) : (
          <ul className="flex flex-col gap-2">
            {list.map((s) => {
              const mine = sameUser(s.createdBy, subject);
              const status = scheduleStatus(s);
              const ch = channelName(s.channelId);
              return (
                <li key={s.scheduleId} className="rounded-[var(--radius)] border border-[var(--color-border)] p-3">
                  <div className="flex items-start gap-2">
                    <div className="min-w-0 flex-1">
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
                    {!s.enabled && mine && (
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy !== null}
                        onClick={() => void act(s, "resume", () => schedulesApi.update(roomId, s.scheduleId, { enabled: true }), "Schedule resumed")}
                      >
                        <PlayIcon size={14} /> Resume
                      </Button>
                    )}
                    {mine && (
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy !== null}
                        onClick={() => void act(s, "run", () => schedulesApi.runNow(roomId, s.scheduleId), "Posted now")}
                      >
                        <PaperPlaneRightIcon size={14} /> Run now
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

// recipientKey identifies a recipient option ("room" or the agent identity).
function recipientKey(r: ScheduleRecipient): string {
  return r.kind === "room" ? "room" : `${r.ownerId}\x00${r.core ?? ""}\x00${r.provider}`;
}

// ScheduleEditor creates or edits one schedule. The recipient choices are the
// composer's agent rows (agentRows: my local targets + the room's advertised
// agents, grouped by core), plus the people-only room.
function ScheduleEditor({
  roomId,
  subject,
  schedule,
  channels,
  roomAgents,
  localTargets,
  onClose,
  onSaved,
}: {
  readonly roomId: string;
  readonly subject: string | null;
  readonly schedule: RoomSchedule | null;
  readonly channels: Channel[];
  readonly roomAgents: RoomAgent[];
  readonly localTargets: readonly PickerTarget[];
  readonly onClose: () => void;
  readonly onSaved: () => void;
}) {
  const toast = useToast();
  const me = subject ?? "";
  const nameOf = useDisplayNames([...roomAgents.map((a) => a.ownerId), schedule?.recipient.kind === "agent" ? schedule.recipient.ownerId : ""]);
  const [text, setText] = useState(schedule?.text ?? "");
  const [timezone, setTimezone] = useState(schedule?.timezone ?? browserTimezone());
  const [channelId, setChannelId] = useState(schedule?.channelId ?? "");
  const [recipient, setRecipient] = useState<ScheduleRecipient>(schedule?.recipient ?? { kind: "room" });
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
    if (recipient.kind === "agent" && !out.some((o) => o.key === recipientKey(recipient))) {
      out.push({ key: recipientKey(recipient), group: "not currently online", label: recipientLabel(recipient, nameOf), recipient });
    }
    return out;
  }, [localTargets, roomAgents, subject, recipient, nameOf]);
  const groups = useMemo(() => [...new Set(options.map((o) => o.group))], [options]);
  const borrowed = isBorrowedRecipient(recipient, me);
  const setKind = (kind: RecurrenceKind) => setForm((f) => ({ ...f, kind }));

  const save = async () => {
    if (!text.trim()) {
      setErr("Write the message to post");
      return;
    }
    const built = recurrenceFromForm(form, timezone);
    if (!built.recurrence) {
      setErr(built.error ?? "Invalid schedule");
      return;
    }
    setErr("");
    setBusy(true);
    const body: ScheduleInput = { text, recipient, recurrence: built.recurrence, timezone, channelId };
    try {
      if (schedule) {
        await schedulesApi.update(roomId, schedule.scheduleId, body);
        toast.success("Schedule saved");
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
      description="Posted as you, through the normal chat path."
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
        <Field label="Message">
          <Textarea autoFocus rows={4} value={text} onChange={(e) => setText(e.target.value)} placeholder="What changed since yesterday? Summarize open questions." />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Send to">
            <Select
              className={SELECT}
              value={recipientKey(recipient)}
              onChange={(e) => {
                const o = options.find((x) => x.key === e.target.value);
                setRecipient(o ? o.recipient : { kind: "room" });
              }}
            >
              <option value="room">The room (people only)</option>
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
              This posts to another member's agent. It runs only while <UserName id={recipient.ownerId} /> has authorized you in this room;
              otherwise the run is refused at drive time.
            </span>
          </p>
        )}
        <Field label="When">
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
