// Scheduled prompts (N2): wire shapes of realtime's /v1/rooms/{id}/schedules
// (internal/wire/schedules.go) and the pure helpers the Schedules panel uses.
// A schedule is a member's own message posted into the room at a time, as
// that member, through the normal chat path.
import { defaultDisplayName, formatAgentHandle } from "./displayNames.ts";

export type { RoomSchedule, ScheduleRecipient, ScheduleRecurrence, ScheduledMarker } from "./wire.gen.ts";
import type { RoomSchedule, ScheduleRecipient, ScheduleRecurrence, ScheduledMarker } from "./wire.gen.ts";

export type { RecurrenceKind, ScheduleRunStatus } from "./wire.gen.ts";
import type { RecurrenceKind } from "./wire.gen.ts";

export interface ScheduleInput {
  text: string;
  channelId?: string;
  recipient: ScheduleRecipient;
  recurrence: ScheduleRecurrence;
  timezone: string;
  enabled?: boolean;
}

export const WEEKDAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;

// The member's browser timezone (the default for a new schedule).
export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

// IANA zones the runtime knows, with the given zone first (it may be absent
// from an older runtime's list).
export function timezoneOptions(preferred: string): string[] {
  let zones: string[] = [];
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
  try {
    zones = intl.supportedValuesOf?.("timeZone") ?? [];
  } catch {
    zones = [];
  }
  const out = new Set<string>([preferred, "UTC", ...zones]);
  return [...out].filter(Boolean);
}

// scheduledMarker reads payload.scheduled from a chat payload, if any.
export function scheduledMarker(payload: unknown): ScheduledMarker | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const s = (payload as { scheduled?: unknown }).scheduled;
  if (!s || typeof s !== "object") return undefined;
  const id = (s as { scheduleId?: unknown }).scheduleId;
  return typeof id === "string" && id ? { scheduleId: id } : undefined;
}

// describeRecurrence renders a recurrence for the list ("Daily at 09:00",
// "Mon, Wed at 17:15", "Once on …", "Cron 0 9 * * 1-5"), in its timezone.
export function describeRecurrence(r: ScheduleRecurrence, timezone: string): string {
  switch (r.kind) {
    case "once":
      return r.atMs ? `Once on ${formatInZone(r.atMs, timezone)}` : "Once";
    case "daily":
      return `Daily at ${r.time ?? "?"}`;
    case "weekly": {
      const days = [...(r.days ?? [])].sort((a, b) => a - b);
      const label =
        days.length === 7
          ? "Every day"
          : days.length === 5 && days.every((d, i) => d === i + 1)
            ? "Weekdays"
            : days.map((d) => WEEKDAYS[d] ?? "?").join(", ");
      return `${label} at ${r.time ?? "?"}`;
    }
    case "cron":
      return `Cron ${r.cron ?? ""}`.trim();
  }
}

// formatInZone renders an instant as "Mon, Oct 12, 09:00" in timezone (the
// browser's own zone when the name is unknown to the runtime).
export function formatInZone(ms: number, timezone: string): string {
  const opts: Intl.DateTimeFormatOptions = {
    weekday: "short",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  };
  try {
    return new Intl.DateTimeFormat(undefined, { ...opts, timeZone: timezone }).format(new Date(ms));
  } catch {
    return new Intl.DateTimeFormat(undefined, opts).format(new Date(ms));
  }
}

// localDateTimeToMs converts a wall-clock "YYYY-MM-DDTHH:MM" in timezone to
// an instant (the inverse of the zone's offset at that time, resolved by one
// correction step so DST boundaries land on the intended wall clock).
export function localDateTimeToMs(local: string, timezone: string): number | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(local.trim());
  if (!m) return undefined;
  const [y, mo, d, h, mi] = m.slice(1).map(Number);
  const wallUtc = Date.UTC(y, mo - 1, d, h, mi);
  const offset = (at: number) => zoneOffsetMs(at, timezone);
  let guess = wallUtc - offset(wallUtc);
  guess = wallUtc - offset(guess);
  return guess;
}

// msToLocalDateTime renders an instant as "YYYY-MM-DDTHH:MM" in timezone (the
// value of a datetime-local input).
export function msToLocalDateTime(ms: number, timezone: string): string {
  const shifted = new Date(ms + zoneOffsetMs(ms, timezone));
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${shifted.getUTCFullYear()}-${pad(shifted.getUTCMonth() + 1)}-${pad(shifted.getUTCDate())}T${pad(
    shifted.getUTCHours(),
  )}:${pad(shifted.getUTCMinutes())}`;
}

// zoneOffsetMs is timezone's UTC offset at instant ms (0 for an unknown zone).
export function zoneOffsetMs(ms: number, timezone: string): number {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: timezone,
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    }).formatToParts(new Date(ms));
    const get = (t: string) => Number(parts.find((p) => p.type === t)?.value ?? 0);
    const asUtc = Date.UTC(get("year"), get("month") - 1, get("day"), get("hour") % 24, get("minute"), get("second"));
    return asUtc - Math.floor(ms / 1000) * 1000;
  } catch {
    return 0;
  }
}

// isBorrowedRecipient reports an agent recipient owned by someone other than
// the scheduling member: the run is a borrow, driven only while the owner's
// per-room authorization allows it (the bridge enforces it at drive time).
export function isBorrowedRecipient(r: ScheduleRecipient, me: string): boolean {
  return r.kind === "agent" && r.ownerId !== me;
}

// recipientLabel renders the recipient by display name ("room",
// "Jack · codex"); the canonical handle stays recipientHandle's job.
export function recipientLabel(r: ScheduleRecipient, nameOf: (id: string) => string = defaultDisplayName): string {
  return r.kind === "agent" ? formatAgentHandle(nameOf(r.ownerId ?? ""), r.provider ?? "") : "room";
}

// scheduleStatus summarizes a schedule's state for the list.
export function scheduleStatus(s: RoomSchedule): { tone: "ok" | "warn" | "muted"; label: string } {
  if (!s.enabled) return { tone: s.lastError ? "warn" : "muted", label: s.lastError ? "Paused — " + s.lastError : "Paused" };
  if (s.lastStatus === "error" || s.lastStatus === "skipped") return { tone: "warn", label: s.lastError || s.lastStatus };
  return { tone: "ok", label: "Active" };
}

// RecurrenceForm is the editor's state for every recurrence kind at once, so
// switching kinds keeps what the member already typed.
export interface RecurrenceForm {
  kind: RecurrenceKind;
  onceLocal: string; // "YYYY-MM-DDTHH:MM" in the schedule's timezone
  time: string; // "HH:MM"
  days: number[];
  cron: string;
}

// formFromRecurrence seeds the editor from a stored recurrence (or defaults:
// daily at 09:00, weekdays, once tomorrow at 09:00).
export function formFromRecurrence(r: ScheduleRecurrence | undefined, timezone: string, nowMs = Date.now()): RecurrenceForm {
  const tomorrow = msToLocalDateTime(nowMs + 24 * 3600 * 1000, timezone).slice(0, 10) + "T09:00";
  return {
    kind: r?.kind ?? "daily",
    onceLocal: r?.kind === "once" && r.atMs ? msToLocalDateTime(r.atMs, timezone) : tomorrow,
    time: r?.time ?? "09:00",
    days: r?.days?.length ? [...r.days] : [1, 2, 3, 4, 5],
    cron: r?.cron ?? "0 9 * * 1-5",
  };
}

// recurrenceFromForm builds the wire recurrence, or a member-facing error.
// The server validates again (minimum interval, cron syntax, DST).
export function recurrenceFromForm(
  f: RecurrenceForm,
  timezone: string,
  nowMs = Date.now(),
): { recurrence?: ScheduleRecurrence; error?: string } {
  const hhmm = /^([01]?\d|2[0-3]):[0-5]\d$/;
  switch (f.kind) {
    case "once": {
      const atMs = localDateTimeToMs(f.onceLocal, timezone);
      if (atMs === undefined) return { error: "Pick a date and time" };
      if (atMs <= nowMs) return { error: "Pick a time in the future" };
      return { recurrence: { kind: "once", atMs } };
    }
    case "daily":
      if (!hhmm.test(f.time)) return { error: "Time must be HH:MM" };
      return { recurrence: { kind: "daily", time: f.time } };
    case "weekly":
      if (!hhmm.test(f.time)) return { error: "Time must be HH:MM" };
      if (f.days.length === 0) return { error: "Pick at least one day" };
      return { recurrence: { kind: "weekly", time: f.time, days: [...new Set(f.days)].sort((a, b) => a - b) } };
    case "cron": {
      const cron = f.cron.trim().split(/\s+/).join(" ");
      if (cron.split(" ").length !== 5) return { error: "Cron needs 5 fields: minute hour day month weekday" };
      return { recurrence: { kind: "cron", cron } };
    }
  }
}
