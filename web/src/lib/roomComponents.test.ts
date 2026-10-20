import { formPayload, type ContentComponent } from "./contentComponents.ts";
import {
  describeRoomInput,
  partKey,
  roomContentForm,
  roomDraftPatch,
  roomSaveFromComponent,
  roomSaveFromDraft,
  roomSaveProblem,
  roomSetupSummary,
  toggleRoomPart,
  type RoomComponentDraft,
  type RoomPackageSchedule,
} from "./roomComponents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const aiTask: RoomPackageSchedule = { channel: "reports", text: "Summarize yesterday.", recipient: { kind: "ai" }, recurrence: { kind: "daily", time: "09:00" }, timezone: "UTC" };
const notice: RoomPackageSchedule = { text: "Week {week}", recipient: { kind: "notice", to: "room", title: "Review" } as RoomPackageSchedule["recipient"], recurrence: { kind: "daily", time: "08:00" }, timezone: "UTC" };

const draft: RoomComponentDraft = {
  roomId: "r1",
  name: "weekly-review",
  displayName: "Weekly review",
  description: "Reviews the week.",
  welcome: "Turn the summary on.",
  quickPrompts: [{ title: "Recap", prompt: "Recap the week" }],
  channels: ["reports", "notes"],
  schedules: [aiTask, notice],
  expert: { id: "cc_e", name: "reviewer", displayName: "Reviewer", version: "1.2.0", ref: "reviewer@^1" },
  skills: [{ id: "cc_s", name: "charts", version: "2.0.0", ref: "charts@^2" }],
  prompts: [{ id: "cc_p", name: "review-prompts", displayName: "Review prompts", version: "1.0.0", ref: "review-prompts@^1" }],
  apps: [{ id: "cc_a", name: "lunch-poll", version: "1.0.0", ref: "lunch-poll@^1" }],
  sources: [],
  left: ["members and their agents"],
};

// A draft starts with everything ticked; its form names every part by its
// reference, and the save payload carries the room's fields: its welcome,
// quick prompts and its own asks for sources too.
{
  const f = roomSaveFromDraft(draft);
  eq(roomSaveProblem(f), null, "a fresh draft saves");
  const form = roomContentForm(f);
  eq([form.welcome, form.quickPrompts], ["Turn the summary on.", [{ title: "Recap", prompt: "Recap the week" }]], "the room's welcome and quick prompts");
  eq(roomSaveFromDraft({ ...draft, sources: [{ name: "runs", purpose: "Judge outcomes." }] }).sources, [{ name: "runs", purpose: "Judge outcomes." }], "the room's asks");
  eq(form.requires, { expert: "reviewer@^1", skills: ["charts@^2"], prompts: ["review-prompts@^1"], mcp: [], permissions: [] }, "requires");
  eq(form.room, { channels: ["reports", "notes"], schedules: [aiTask, notice], apps: ["lunch-poll@^1"], sources: [] }, "room setup");
  const asked = roomContentForm({ ...f, sources: [{ name: "runs", purpose: " Judge outcomes. " }] });
  const payload = formPayload({ ...asked, welcome: " Hi " });
  eq(
    [payload.kind, payload.welcome, payload.channels, payload.apps, payload.sources, (payload.requires as Record<string, unknown>).expert, "instructions" in payload],
    ["room", "Hi", ["reports", "notes"], ["lunch-poll@^1"], [{ name: "runs", purpose: "Judge outcomes." }], "reviewer@^1", false],
    "the payload is a room package's: no instructions",
  );
}

// Unticked parts stay out; a kept schedule in a dropped channel is refused
// until the member keeps one or the other.
{
  let f = roomSaveFromDraft(draft);
  f = toggleRoomPart(f, partKey("skill", "charts@^2"), false);
  f = toggleRoomPart(f, partKey("expert"), false);
  f = toggleRoomPart(f, partKey("schedule", 1), false);
  f = toggleRoomPart(f, partKey("channel", "notes"), false);
  const form = roomContentForm(f);
  eq([form.requires.expert, form.requires.skills, form.room?.schedules, form.room?.channels], [undefined, [], [aiTask], ["reports"]], "unticked parts stay out");
  const dropped = toggleRoomPart(f, partKey("channel", "reports"), false);
  assert((roomSaveProblem(dropped) ?? "").includes("#reports"), "a kept schedule in a dropped channel is refused");
  eq(roomSaveProblem(toggleRoomPart(dropped, partKey("schedule", 0), false)), null, "dropping the schedule too saves");
  eq(toggleRoomPart(dropped, partKey("channel", "reports"), true).off.includes(partKey("channel", "reports")), false, "ticking again keeps it");
  assert(roomSaveProblem({ ...f, name: "Weekly Review" }) !== null, "a bad name is refused");
  assert(roomSaveProblem({ ...f, description: "d".repeat(501) }) !== null, "a description past a room's is refused");
  assert(roomSaveProblem({ ...f, category: "two words" }) !== null, "a category is one word");
  assert((roomSaveProblem({ ...f, sources: [{ name: "runs", purpose: "  " }] }) ?? "").includes("organization admin"), "a source needs its purpose");
  assert(roomSaveProblem({ ...f, sources: [{ name: "runs", purpose: "p".repeat(501) }] }) !== null, "a purpose past its bound is refused");
  eq(roomSaveProblem({ ...f, sources: [{ name: "runs", purpose: "Judge outcomes." }] }), null, "a source with its purpose saves");
}

// A component's next version starts from its own setup.
{
  const c = {
    kind: "room",
    name: "weekly-review",
    displayName: "Weekly review",
    description: "Reviews.",
    tags: ["reviews", "weekly"],
    welcome: "Hello",
    quickPrompts: [{ title: "Plan", prompt: "Plan it." }],
    requires: { expert: "reviewer@^1", prompts: ["review-prompts@^1"], mcp: [{ name: "browser" }], permissions: ["mcp.stdio"] },
    room: { channels: ["reports"], schedules: [aiTask], apps: [], sources: [{ name: "runs", purpose: "Judge outcomes." }] },
  } as unknown as ContentComponent;
  const form = roomContentForm(roomSaveFromComponent(c));
  eq(
    [form.tags, form.welcome, form.quickPrompts, form.requires, form.room?.sources],
    [["reviews", "weekly"], "Hello", [{ title: "Plan", prompt: "Plan it." }], { expert: "reviewer@^1", skills: [], prompts: ["review-prompts@^1"], mcp: [{ name: "browser" }], permissions: ["mcp.stdio"] }, [{ name: "runs", purpose: "Judge outcomes." }]],
    "a new version keeps what the dialog does not edit",
  );
}

// The summary.
eq(roomSetupSummary(undefined, {}), "An empty room", "nothing set up");
eq(
  roomSetupSummary({ channels: ["a"], schedules: [aiTask, notice], apps: ["x@^1"], sources: [] }, { expert: "e@^1", skills: ["s@^1"], prompts: ["p@^1"] }),
  "1 channel · 2 scheduled tasks · an expert · 2 packs · 1 app",
  "summary",
);

// describe-room reads the setup and the member's values; its reply fills
// the dialog within the rules.
{
  const f = roomSaveFromDraft(draft);
  const input = JSON.parse(describeRoomInput(f)) as { setup: { scheduledTasks: string[]; expert: string }; current: { displayName: string } };
  eq([input.setup.expert, input.setup.scheduledTasks.length, input.current.displayName], ["Reviewer", 2, "Weekly review"], "describe-room input");
  eq(
    roomDraftPatch({ displayName: "Release review", description: "x".repeat(600), category: "Reviews", tags: ["Release", "bad,tag!", "release"], welcome: " Turn it on. " }, { ...f, name: "weekly-review" }),
    { displayName: "Release review", name: "release-review", description: "x".repeat(500), category: "reviews", tags: "release", welcome: "Turn it on." },
    "a reply fills the fields: the name follows an untouched display name, the description a room's bound",
  );
  eq(roomDraftPatch({ displayName: "Release review", category: "two words" }, { ...f, name: "my-own" }), { displayName: "Release review" }, "a changed name is kept; a bad category dropped");
}
