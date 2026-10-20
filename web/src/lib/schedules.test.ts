import {
  PLATFORM_EVENTS,
  describeRecurrence,
  formFromRecurrence,
  recurrenceFromForm,
  isBorrowedRecipient,
  noticeTargetLabel,
  noticeToOf,
  noticeWhereOf,
  localDateTimeToMs,
  msToLocalDateTime,
  recipientLabel,
  scheduledMarker,
  scheduleStatus,
  agentToPick,
  waitsForAgent,
  type RoomSchedule,
} from "./schedules.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq(describeRecurrence({ kind: "daily", time: "09:00" }, "UTC"), "Daily at 09:00", "daily");
eq(describeRecurrence({ kind: "weekly", time: "17:15", days: [5, 1, 3] }, "UTC"), "Mon, Wed, Fri at 17:15", "weekly");
eq(describeRecurrence({ kind: "weekly", time: "08:00", days: [1, 2, 3, 4, 5] }, "UTC"), "Weekdays at 08:00", "weekdays");
eq(describeRecurrence({ kind: "cron", cron: "0 9 * * 1-5" }, "UTC"), "Cron 0 9 * * 1-5", "cron");
assert(describeRecurrence({ kind: "once", atMs: Date.UTC(2026, 11, 1, 8, 0) }, "UTC").startsWith("Once on"), "once");

// Wall clock <-> instant in a zone, including both sides of a DST change.
eq(localDateTimeToMs("2026-03-07T09:00", "America/New_York"), Date.UTC(2026, 2, 7, 14, 0), "EST wall clock");
eq(localDateTimeToMs("2026-03-09T09:00", "America/New_York"), Date.UTC(2026, 2, 9, 13, 0), "EDT wall clock");
eq(localDateTimeToMs("2026-10-08T09:30", "Asia/Kolkata"), Date.UTC(2026, 9, 8, 4, 0), "half-hour zone");
eq(localDateTimeToMs("nope", "UTC"), undefined, "invalid input");
eq(msToLocalDateTime(Date.UTC(2026, 2, 9, 13, 0), "America/New_York"), "2026-03-09T09:00", "instant to wall clock");
eq(msToLocalDateTime(Date.UTC(2026, 0, 1, 0, 0), "UTC"), "2026-01-01T00:00", "utc");

eq(scheduledMarker({ text: "x", scheduled: { scheduleId: "sch_1" } }), { scheduleId: "sch_1" }, "marker");
eq(scheduledMarker({ text: "x" }), undefined, "no marker");
eq(scheduledMarker({ scheduled: { scheduleId: 5 } }), undefined, "bad marker");

assert(isBorrowedRecipient({ kind: "agent", ownerId: "bob", provider: "codex" }, "alice"), "another member's agent is a borrow");
assert(!isBorrowedRecipient({ kind: "agent", ownerId: "alice", provider: "codex" }, "alice"), "my agent is not a borrow");
assert(!isBorrowedRecipient({ kind: "room" }, "alice"), "room is not a borrow");
eq(recipientLabel({ kind: "agent", ownerId: "bob", provider: "codex" }), "bob · codex", "agent label");
eq(recipientLabel({ kind: "agent", ownerId: "bob@corp.example.com", provider: "codex" }), "bob · codex", "agent label never shows the address");
eq(recipientLabel({ kind: "agent", ownerId: "bob@x.io", provider: "codex" }, () => "Bob"), "Bob · codex", "resolved name");

const base: RoomSchedule = {
  scheduleId: "s",
  tenantId: "t",
  roomId: "r",
  createdBy: "alice",
  text: "x",
  recipient: { kind: "room" },
  recurrence: { kind: "daily", time: "09:00" },
  timezone: "UTC",
  enabled: true,
  runCount: 0,
  createdAtMs: 0,
  updatedAtMs: 0,
};
eq(scheduleStatus(base).tone, "ok", "active");
eq(scheduleStatus({ ...base, enabled: false }).label, "Paused", "paused");
eq(scheduleStatus({ ...base, enabled: false, lastError: "the room no longer exists" }).tone, "warn", "auto-paused");

// A request to an agent that names none waits for its creator's pick.
{
  const waiting: RoomSchedule = { ...base, scheduleId: "w", enabled: false, recipient: { kind: "agent" }, createdAtMs: 2 };
  assert(waitsForAgent(waiting.recipient), "an agent recipient without its agent waits");
  assert(!waitsForAgent({ kind: "agent", ownerId: "bob", provider: "codex" }), "a named agent does not wait");
  assert(!waitsForAgent({ kind: "ai" }) && !waitsForAgent({ kind: "room" }), "other kinds never wait");
  eq(scheduleStatus(waiting), { tone: "warn", label: "Waiting for an agent" }, "waiting status");
  eq(recipientLabel(waiting.recipient), "an agent you pick", "waiting label");
  assert(!isBorrowedRecipient(waiting.recipient, "alice"), "an agent not picked yet is nobody's borrow");
  const older = { ...waiting, scheduleId: "v", createdAtMs: 1 };
  const theirs = { ...waiting, scheduleId: "x", createdBy: "bob" };
  eq(agentToPick([waiting, base, theirs, older], "alice").map((s) => s.scheduleId), ["v", "w"], "my waiting schedules, oldest first");
  eq(agentToPick([waiting], null), [], "nobody signed in picks nothing");
}

// Editor <-> wire recurrence.
{
  const now = Date.UTC(2026, 9, 8, 12, 0);
  const f = formFromRecurrence(undefined, "UTC", now);
  eq([f.kind, f.time, f.days, f.onceLocal], ["daily", "09:00", [1, 2, 3, 4, 5], "2026-10-09T09:00"], "default form");
  eq(recurrenceFromForm(f, "UTC", now), { recurrence: { kind: "daily", time: "09:00" } }, "daily from form");
  eq(recurrenceFromForm({ ...f, kind: "weekly", days: [5, 1, 5] }, "UTC", now).recurrence, { kind: "weekly", time: "09:00", days: [1, 5] }, "weekly from form");
  eq(recurrenceFromForm({ ...f, kind: "weekly", days: [] }, "UTC", now).error, "Pick at least one day", "weekly needs days");
  eq(recurrenceFromForm({ ...f, time: "25:00" }, "UTC", now).error, "Time must be HH:MM", "bad time");
  eq(recurrenceFromForm({ ...f, kind: "once" }, "America/New_York", now).recurrence, { kind: "once", atMs: Date.UTC(2026, 9, 9, 13, 0) }, "once in zone");
  eq(recurrenceFromForm({ ...f, kind: "once", onceLocal: "2026-10-01T09:00" }, "UTC", now).error, "Pick a time in the future", "once in the past");
  eq(recurrenceFromForm({ ...f, kind: "cron", cron: " 0  9 * * 1-5 " }, "UTC", now).recurrence, { kind: "cron", cron: "0 9 * * 1-5" }, "cron normalized");
  eq(recurrenceFromForm({ ...f, kind: "cron", cron: "0 9 *" }, "UTC", now).error?.startsWith("Cron needs"), true, "cron field count");
  const round = formFromRecurrence({ kind: "once", atMs: Date.UTC(2026, 9, 9, 13, 0) }, "America/New_York", now);
  eq([round.kind, round.onceLocal], ["once", "2026-10-09T09:00"], "once round trip");
}

// A notice's target round-trips through the editor's fields.
eq(noticeWhereOf("room"), { target: "room", group: "", logins: "" }, "room target");
eq(noticeWhereOf("group:Infra"), { target: "group", group: "Infra", logins: "" }, "group target");
eq(noticeWhereOf(["maya", "jack"]), { target: "logins", group: "", logins: "maya, jack" }, "logins target");
eq(noticeWhereOf(undefined), { target: "room", group: "", logins: "" }, "no target reads as the room");
eq(noticeToOf({ target: "room", group: "x", logins: "y" }), "room", "room ignores the other fields");
eq(noticeToOf({ target: "group", group: " Infra ", logins: "" }), "group:Infra", "a group name is trimmed");
eq(noticeToOf({ target: "group", group: "  ", logins: "" }), undefined, "a group target needs a name");
eq(noticeToOf({ target: "logins", group: "", logins: "maya, jack;maya  nora" }), ["maya", "jack", "nora"], "logins split on commas, semicolons and spaces, once each");
eq(noticeToOf({ target: "logins", group: "", logins: " , " }), undefined, "a logins target needs someone");
eq(noticeTargetLabel("group:Infra"), "group Infra", "group label");
eq(noticeTargetLabel(["maya", "jack"]), "maya, jack", "logins label");
eq(noticeTargetLabel("room"), "the room", "room label");
eq(recipientLabel({ kind: "notice", to: "group:Infra", title: "t" }), "notice to group Infra", "a notice's recipient label");

// An event schedule (docs/prd/2026-10-20-event-triggered-schedules.md): the
// editor offers the server's closed list, sends the events in that order,
// and a component name only with component publishing.
{
  const now = Date.UTC(2026, 9, 8, 12, 0);
  eq(
    PLATFORM_EVENTS.map((e) => e.value),
    ["platform.deployed", "component.published", "desktop.published"],
    "the events are the server's list, in its order",
  );
  const f = { ...formFromRecurrence(undefined, "UTC", now), kind: "event" as const };
  eq(f.events, ["platform.deployed"], "a new event schedule follows deploys");
  eq(recurrenceFromForm(f, "UTC", now).recurrence, { kind: "event", events: ["platform.deployed"] }, "deploys");
  eq(
    recurrenceFromForm({ ...f, events: ["desktop.published", "component.published"], component: " browser " }, "UTC", now).recurrence,
    { kind: "event", events: ["component.published", "desktop.published"], component: "browser" },
    "events in the list's order, the component trimmed",
  );
  eq(
    recurrenceFromForm({ ...f, events: ["platform.deployed"], component: "browser" }, "UTC", now).recurrence,
    { kind: "event", events: ["platform.deployed"] },
    "a component name only goes with component publishing",
  );
  eq(recurrenceFromForm({ ...f, events: [] }, "UTC", now).error, "Pick at least one change to run on", "an event schedule follows something");
  const round = formFromRecurrence({ kind: "event", events: ["component.published"], component: "browser" }, "UTC", now);
  eq([round.kind, round.events, round.component], ["event", ["component.published"], "browser"], "event round trip");
  eq(describeRecurrence({ kind: "event", events: ["platform.deployed", "desktop.published"] }, "UTC"), "When bnbloc is deployed or a desktop version is published", "events described");
  eq(describeRecurrence({ kind: "event", events: ["component.published"], component: "browser" }, "UTC"), "When component browser is published", "a component filter described");
}
