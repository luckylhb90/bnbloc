import {
  EVENT_SOURCES,
  describeRecurrence,
  hookInstructions,
  hookURL,
  sourceTypes,
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

// An event trigger (docs/prd/2026-10-21-room-events.md): the platform's and
// the room's types are the server's closed lists, a member's and a
// webhook's are typed; the subject filter is exact or a prefix ending in *,
// the data filter field=value lines; a member never takes a platform type.
{
  const now = Date.UTC(2026, 9, 8, 12, 0);
  eq(
    EVENT_SOURCES.map((s) => s.value),
    ["bnbloc/platform", "bnbloc/room", "bnbloc/member", "webhook"],
    "the sources are the server's list, in its order",
  );
  eq(sourceTypes("bnbloc/platform").map((t) => t.value), ["platform.deployed", "component.published", "desktop.published"], "the platform's types");
  eq(sourceTypes("bnbloc/room").map((t) => t.label), ["a file is added or updated", "someone joins", "an app's data changes"], "the room's types in words");
  eq(sourceTypes("webhook"), [], "a webhook's types are open");

  const f = { ...formFromRecurrence(undefined, "UTC", now), kind: "event" as const };
  eq([f.source, f.types], ["bnbloc/room", ["room.file.added"]], "a new trigger takes the room's files");
  eq(recurrenceFromForm(f, "UTC", now).recurrence, { kind: "event", source: "bnbloc/room", types: ["room.file.added"] }, "no filter");
  eq(
    recurrenceFromForm({ ...f, types: ["room.member.joined", "room.file.added"], subject: " reports/* " }, "UTC", now).recurrence,
    { kind: "event", source: "bnbloc/room", types: ["room.file.added", "room.member.joined"], filter: { subjectPrefix: "reports/" } },
    "types in the list's order; a trailing * is a prefix",
  );
  const hook = { ...f, source: "webhook" as const, typesText: "com.github.push, ci.*\ncom.github.push", subject: "", dataFilter: "ref = refs/heads/main\n\nrepo=bnbloc" };
  eq(
    recurrenceFromForm(hook, "UTC", now).recurrence,
    { kind: "event", source: "webhook", types: ["com.github.push", "ci.*"], filter: { data: { ref: "refs/heads/main", repo: "bnbloc" } } },
    "typed types, unique; data fields",
  );
  eq(recurrenceFromForm({ ...hook, typesText: "" }, "UTC", now).error, "Name at least one event type", "an open source needs a type");
  eq(recurrenceFromForm({ ...hook, typesText: "com.*.push" }, "UTC", now).error, "A * goes only at the end of a type", "a star inside");
  eq(recurrenceFromForm({ ...hook, dataFilter: "ref" }, "UTC", now).error, "Write each data filter as field=value (ref)", "a data line without =");
  eq(recurrenceFromForm({ ...f, types: [] }, "UTC", now).error, "Pick at least one event to run on", "a closed source needs a pick");
  assert(
    (recurrenceFromForm({ ...hook, source: "bnbloc/member", typesText: "room.file.added" }, "UTC", now).error ?? "").startsWith("A member's event never starts with platform."),
    "a member never sends the platform's types",
  );
  const round = formFromRecurrence({ kind: "event", source: "webhook", types: ["com.github.push"], filter: { subjectPrefix: "v1.", data: { ref: "main" } } }, "UTC", now);
  eq([round.source, round.typesText, round.subject, round.dataFilter], ["webhook", "com.github.push", "v1.*", "ref=main"], "a webhook trigger round trip");
  eq(describeRecurrence({ kind: "event", source: "bnbloc/platform", types: ["platform.deployed", "desktop.published"] }, "UTC"), "When bnbloc is deployed or a desktop version is published", "platform events described");
  eq(
    describeRecurrence({ kind: "event", source: "bnbloc/room", types: ["room.file.added"], filter: { subjectPrefix: "reports/" } }, "UTC"),
    "When a file is added or updated in this room (subject starts with reports/)",
    "a room trigger described",
  );
  eq(
    describeRecurrence({ kind: "event", source: "webhook", types: ["com.github.push"], filter: { data: { ref: "refs/heads/main" } } }, "UTC"),
    "When another system sends com.github.push (ref = refs/heads/main)",
    "a webhook trigger described",
  );
  eq(describeRecurrence({ kind: "event", source: "bnbloc/member", types: ["dev.finished"] }, "UTC"), "When a member sends dev.finished", "a member trigger described");
  eq(hookURL("https://bnbloc.example.com/", "hk_1"), "https://bnbloc.example.com/v1/hooks/hk_1", "the webhook URL");
  const told = hookInstructions("https://bnbloc.example.com/v1/hooks/hk_1", "whsec_x");
  assert(told.includes("Secret: whsec_x") && told.includes("X-Bnbloc-Signature: sha256=") && told.includes("X-Hub-Signature-256"), "the sender is told how to sign");
}

