// Scheduled prompts (N2): wire shapes of realtime's /v1/rooms/{id}/schedules
// (internal/wire/schedules.go) and the pure helpers the Schedules panel uses.
// A schedule is a member's own message posted into the room at a time, as
// that member, through the normal chat path.
import { defaultDisplayName, formatAgentHandle } from "./displayNames.ts";

export type { RoomSchedule, ScheduleRecipient, ScheduleRecurrence, ScheduledMarker } from "./wire.gen.ts";
import type { RoomSchedule, ScheduleRecipient, ScheduleRecurrence, ScheduledMarker } from "./wire.gen.ts";

export type { EventSubscriptionSource, RecurrenceKind } from "./wire.gen.ts";
import type { EventFilter, EventSubscriptionSource, RecurrenceKind } from "./wire.gen.ts";
import {
  EventSubscriptionSourceValues,
  GitHubEventHeader,
  GitHubSignatureHeader,
  HookEventHeader,
  HookPathPrefix,
  HookSignatureHeader,
  PlatformEventTypes,
  ReservedEventTypePrefixes,
  RoomEventTypes,
} from "./wire.gen.ts";

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

// What each event a trigger may take is called in the editor and the list.
// The platform's and the room's types are the server's closed lists
// (wire.PlatformEventTypes, wire.RoomEventTypes); a type with no words
// here shows as itself.
const EVENT_LABELS: Record<string, string> = {
  "platform.deployed": "bnbloc is deployed",
  "component.published": "a component is published",
  "desktop.published": "a desktop version is published",
  "room.file.added": "a file is added or updated",
  "room.member.joined": "someone joins",
  "room.app.data.changed": "an app's data changes",
};

// Where a trigger's events come from, in words.
const SOURCE_LABELS: Record<EventSubscriptionSource, string> = {
  "bnbloc/platform": "The platform",
  "bnbloc/room": "This room",
  "bnbloc/member": "A member of this room",
  webhook: "Another system (webhook)",
};

/** Where a trigger's events may come from, in the server's order. */
export const EVENT_SOURCES: readonly { readonly value: EventSubscriptionSource; readonly label: string }[] = EventSubscriptionSourceValues.map(
  (value) => ({ value, label: SOURCE_LABELS[value] }),
);

/** The closed list of a source's types (an open source has none: the
 *  member types them). */
export function sourceTypes(source: EventSubscriptionSource): readonly { readonly value: string; readonly label: string }[] {
  const list = source === "bnbloc/platform" ? PlatformEventTypes : source === "bnbloc/room" ? RoomEventTypes : [];
  return list.map((value) => ({ value, label: EVENT_LABELS[value] ?? value }));
}

// describeRecurrence renders a recurrence for the list ("Daily at 09:00",
// "Mon, Wed at 17:15", "Once on …", "Cron 0 9 * * 1-5", "When bnbloc is
// deployed"), in its timezone.
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
    case "event":
      return describeTrigger(r);
  }
}

// describeTrigger renders an event trigger: "When a file is added or
// updated in this room (subject starts with reports/)", "When another system
// sends com.github.push (ref = refs/heads/main)".
export function describeTrigger(r: ScheduleRecurrence): string {
  const types = (r.types ?? []).map((t) => EVENT_LABELS[t] ?? t).join(" or ");
  const where = filterWords(r.filter);
  const tail = where ? ` (${where})` : "";
  switch (r.source) {
    case "bnbloc/platform":
      return `When ${types || "the platform changes"}${tail}`;
    case "bnbloc/room":
      return `When ${types || "something happens"} in this room${tail}`;
    case "bnbloc/member":
      return `When a member sends ${types || "an event"}${tail}`;
    case "webhook":
      return `When another system sends ${types || "an event"}${tail}`;
  }
  return `When ${types || "an event happens"}${tail}`;
}

function filterWords(f: EventFilter | undefined): string {
  if (!f) return "";
  const parts: string[] = [];
  if (f.subject) parts.push(`subject is ${f.subject}`);
  if (f.subjectPrefix) parts.push(`subject starts with ${f.subjectPrefix}`);
  for (const [k, v] of Object.entries(f.data ?? {}).sort(([a], [b]) => a.localeCompare(b))) parts.push(`${k} = ${v}`);
  return parts.join(", ");
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
  return r.kind === "agent" && !waitsForAgent(r) && r.ownerId !== me;
}

// waitsForAgent reports a request to an agent that names none yet (a room
// package's, until its installer picks one of their agents): it is never
// turned on or run (the server's schedule.WaitsForAgent decides; this
// only shows it).
export function waitsForAgent(r: ScheduleRecipient): boolean {
  return r.kind === "agent" && !r.ownerId && !r.provider && !r.core;
}

// agentToPick is what a member still has to do in a room: their own
// schedules that wait for them to pick an agent, oldest first.
export function agentToPick(list: readonly RoomSchedule[], me: string | null): RoomSchedule[] {
  return list.filter((s) => !!me && s.createdBy === me && waitsForAgent(s.recipient)).sort((a, b) => a.createdAtMs - b.createdAtMs);
}

// recipientLabel renders the recipient by display name ("room",
// "Jack · codex"); the canonical handle stays recipientHandle's job.
export function recipientLabel(r: ScheduleRecipient, nameOf: (id: string) => string = defaultDisplayName): string {
  if (r.kind === "ai") return "AI";
  if (r.kind === "notice") return `notice to ${noticeTargetLabel(r.to)}`;
  if (waitsForAgent(r)) return "an agent you pick";
  return r.kind === "agent" ? formatAgentHandle(nameOf(r.ownerId ?? ""), r.provider ?? "") : "room";
}

// A notice schedule's target is what an app's notify takes: "room", a list of
// logins, or "group:<name>" (a group chat the room connected).
export type NoticeTarget = "room" | "group" | "logins";

export interface NoticeWhere {
  readonly target: NoticeTarget;
  readonly group: string;
  readonly logins: string;
}

const GROUP_PREFIX = "group:";

/** The target as the editor holds it (a group's name, the logins one per comma). */
export function noticeWhereOf(to: unknown): NoticeWhere {
  if (Array.isArray(to)) return { target: "logins", group: "", logins: to.filter((x) => typeof x === "string").join(", ") };
  if (typeof to === "string" && to.startsWith(GROUP_PREFIX)) return { target: "group", group: to.slice(GROUP_PREFIX.length), logins: "" };
  return { target: "room", group: "", logins: "" };
}

/** The target to send: undefined when what the member picked is not filled in. */
export function noticeToOf(w: NoticeWhere): unknown {
  switch (w.target) {
    case "room":
      return "room";
    case "group":
      return w.group.trim() === "" ? undefined : GROUP_PREFIX + w.group.trim();
    case "logins": {
      const logins = [...new Set(w.logins.split(/[\s,;]+/).filter((x) => x !== ""))];
      return logins.length === 0 ? undefined : logins;
    }
  }
}

/** A notice's target in words, for the list. */
export function noticeTargetLabel(to: unknown): string {
  const w = noticeWhereOf(to);
  if (w.target === "group") return `group ${w.group}`;
  if (w.target === "logins") return w.logins;
  return "the room";
}

// scheduleStatus summarizes a schedule's state for the list.
export function scheduleStatus(s: RoomSchedule): { tone: "ok" | "warn" | "muted"; label: string } {
  if (waitsForAgent(s.recipient)) return { tone: "warn", label: "Waiting for an agent" };
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
  // An event trigger: where its events come from, the types it takes (the
  // picked ones of a closed list; typed, one per line or comma, for an open
  // source), and its filter: the subject (exact, or a prefix ending in *)
  // and top-level data fields ("field=value", one per line).
  source: EventSubscriptionSource;
  types: string[];
  typesText: string;
  subject: string;
  dataFilter: string;
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
    source: r?.source ?? "bnbloc/room",
    types: r?.types?.length ? [...r.types] : ["room.file.added"],
    typesText: r?.source === "webhook" || r?.source === "bnbloc/member" ? (r.types ?? []).join("\n") : "",
    subject: r?.filter?.subject ?? (r?.filter?.subjectPrefix ? r.filter.subjectPrefix + "*" : ""),
    dataFilter: Object.entries(r?.filter?.data ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
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
    case "event":
      return triggerFromForm(f);
  }
}

// triggerFromForm builds an event trigger from the editor, or a
// member-facing error. The server validates again (known types, bounds).
function triggerFromForm(f: RecurrenceForm): { recurrence?: ScheduleRecurrence; error?: string } {
  const closed = sourceTypes(f.source);
  const types =
    closed.length > 0
      ? closed.map((t) => t.value).filter((t) => f.types.includes(t))
      : [...new Set(f.typesText.split(/[\s,]+/).filter((t) => t !== ""))];
  if (types.length === 0) return { error: closed.length > 0 ? "Pick at least one event to run on" : "Name at least one event type" };
  if (types.some((t) => t.includes("*") && t.indexOf("*") !== t.length - 1)) return { error: "A * goes only at the end of a type" };
  if (f.source === "bnbloc/member" && types.some((t) => ReservedEventTypePrefixes.some((p) => t.startsWith(p)))) {
    return { error: `A member's event never starts with ${ReservedEventTypePrefixes.join(" ")}` };
  }
  const filter: EventFilter = {};
  const subject = f.subject.trim();
  if (subject.endsWith("*")) filter.subjectPrefix = subject.slice(0, -1);
  else if (subject) filter.subject = subject;
  for (const line of f.dataFilter.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const at = t.indexOf("=");
    if (at <= 0) return { error: `Write each data filter as field=value (${t})` };
    filter.data = { ...filter.data, [t.slice(0, at).trim()]: t.slice(at + 1).trim() };
  }
  const hasFilter = !!(filter.subject || filter.subjectPrefix || filter.data);
  return { recurrence: { kind: "event", source: f.source, types, ...(hasFilter ? { filter } : {}) } };
}

/** The webhook URL of a hook on this deployment (origin "" is same-origin). */
export function hookURL(origin: string, hookId: string): string {
  return `${origin.replace(/\/+$/, "")}${HookPathPrefix}${encodeURIComponent(hookId)}`;
}

/** What a hook's sender is told: the URL, the secret, and how to sign. */
export function hookInstructions(url: string, secret: string): string {
  return [
    `URL: ${url}`,
    `Secret: ${secret}`,
    `Sign each POST: ${HookSignatureHeader}: sha256=<hex HMAC-SHA256 of the raw body with the secret> (GitHub's ${GitHubSignatureHeader} works as is).`,
    `Name the event in ${HookEventHeader} (GitHub's ${GitHubEventHeader} reads as com.github.<event>), or as "type" in the JSON body.`,
  ].join("\n");
}
