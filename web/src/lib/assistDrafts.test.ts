import {
  draftRequestText,
  expertDraftPatch,
  fieldItems,
  fieldList,
  filledForm,
  findByName,
  promptPackDraftPatch,
  roomPlanFrom,
  scheduleDraftFrom,
  skillPackDraftPatch,
} from "./assistDrafts.ts";
import { emptyContentForm, type ContentComponent } from "./contentComponents.ts";
import { emptyPromptPackForm } from "./promptPacks.ts";
import { MaxAssistText } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function comp(p: Partial<ContentComponent> & Pick<ContentComponent, "id" | "name" | "kind">): ContentComponent {
  return { scope: "personal", owner: "maya", status: "active", latestVersion: "1.0.0", version: "1.0.0", permissions: [], requires: {}, ...p } as ContentComponent;
}

// Reading fields: lists from arrays or one comma string, items as string records.
{
  eq(fieldList({ tags: ["a", " b ", 3, ""] }, "tags"), ["a", "b"], "a list keeps strings");
  eq(fieldList({ tags: "a, b\nc" }, "tags"), ["a", "b", "c"], "a list given as one string");
  eq(fieldItems({ xs: [{ title: " T ", n: 1 }, "no", null, ["x"], { prompt: "P" }] }, "xs"), [{ title: "T" }, { prompt: "P" }], "items keep string fields of objects");
  eq(fieldItems({ xs: "nope" }, "xs"), [], "items need an array");
}

// The text a draft task is asked with.
{
  eq(draftRequestText(" a sentence ", null), "a sentence", "no current form");
  eq(draftRequestText("a sentence", {}), "a sentence", "an empty form adds nothing");
  eq(draftRequestText("ask", { displayName: "X" }), 'ask\n\nCurrent form:\n{\n "displayName": "X"\n}', "the current form as JSON");
  const long = draftRequestText("ask", { instructions: "x".repeat(20000) });
  assert(long.length <= MaxAssistText && long.endsWith("…"), "a long form is cut to the bound");
  const form = emptyContentForm("expert");
  form.displayName = " Reviewer ";
  form.quickPrompts = [{ title: "", prompt: "" }, { title: "Go", prompt: "Review it" }];
  eq(filledForm(form), { displayName: "Reviewer", quickPrompts: [{ title: "Go", prompt: "Review it" }] }, "only what the form holds");
}

// draft-expert: the expert's fields, quick prompts within bounds, tags by the tag rule, a name only for a new unnamed expert.
{
  const fields = {
    displayName: "Code Reviewer",
    description: "Reviews pull requests",
    instructions: "You review code.",
    welcome: "Hi",
    quickPrompts: [{ title: "Review", prompt: "Review this PR" }, { title: "", prompt: "untitled" }, { title: "x".repeat(100), prompt: "long title" }],
    tags: ["Code Review", "BAD!", "代码"],
  };
  const patch = expertDraftPatch(fields, emptyContentForm("expert"), true);
  eq(patch.name, "code-reviewer", "a name from the display name");
  eq(patch.instructions, "You review code.", "instructions");
  eq(patch.quickPrompts?.length, 2, "an untitled quick prompt is dropped");
  eq(patch.quickPrompts?.[1].title.length, 80, "a title is cut to 80");
  eq(patch.tags, ["code review", "代码"], "tags the rule admits");
  const named = { ...emptyContentForm("expert"), name: "mine" };
  eq(expertDraftPatch(fields, named, true).name, undefined, "a named expert keeps its name");
  eq(expertDraftPatch({ displayName: "代码评审" }, emptyContentForm("expert"), true).name, undefined, "no latin name to make");
  eq(expertDraftPatch(fields, emptyContentForm("expert"), false).name, undefined, "a new version keeps its name");
  eq(expertDraftPatch({}, emptyContentForm("expert"), true), {}, "an empty reply changes nothing");
}

// draft-skill-pack: skills with valid, unique names; empty ones dropped.
{
  const patch = skillPackDraftPatch(
    { displayName: "Lint kit", skills: [{ name: "Run Lint", description: "Use to lint", body: "Run it" }, { name: "run lint", description: "Again" }, { name: "", body: "Body" }, { name: "empty" }] },
    emptyContentForm("skill"),
    true,
  );
  eq(
    patch.skills,
    [
      { name: "run-lint", description: "Use to lint", body: "Run it" },
      { name: "run-lint-2", description: "Again", body: "" },
      { name: "skill", description: "", body: "Body" },
    ],
    "skills normalized",
  );
  eq(patch.name, "lint-kit", "the pack's name");
}

// draft-prompt-pack: prompts with unique ids and their variables.
{
  const patch = promptPackDraftPatch(
    { displayName: "Release notes", prompts: [{ title: "Notes", body: "Write notes for {{version}}" }, { title: "Notes", body: "Again" }, { title: "No body" }] },
    emptyPromptPackForm(),
    true,
  );
  eq(
    patch.prompts?.map((p) => [p.id, p.title, p.variables.map((v) => v.name)]),
    [
      ["notes", "Notes", ["version"]],
      ["notes-2", "Notes", []],
    ],
    "prompts",
  );
}

// A new version keeps a same-titled prompt's id, command and starter flag.
{
  const form = emptyPromptPackForm();
  form.prompts = [{ id: "weekly", title: "Weekly report", body: "old", slash: "weekly", tags: "report", variables: [], starter: true }];
  const patch = promptPackDraftPatch({ prompts: [{ title: "weekly report", body: "New {{team}}" }, { title: "Retro", body: "Run a retro" }] }, form, false);
  eq(
    patch.prompts?.map((p) => [p.id, p.slash, p.starter, p.body, p.variables.map((v) => v.name)]),
    [
      ["weekly", "weekly", true, "New {{team}}", ["team"]],
      ["retro", "", false, "Run a retro", []],
    ],
    "kept by title",
  );
  eq(patch.name, undefined, "a new version keeps the pack's name");
}

// plan-room: the room id, and the expert and skills found in the library (unknown names dropped).
{
  const experts = [comp({ id: "e1", name: "reviewer", displayName: "Code reviewer", kind: "expert" })];
  const skills = [comp({ id: "s1", name: "lint-kit", kind: "skill" }), comp({ id: "s2", name: "docs", displayName: "Doc writer", kind: "skill" })];
  const plan = roomPlanFrom(
    { roomId: "q3-review", name: "Q3 评审", description: "Review", expert: "code reviewer", skills: ["lint-kit", "Doc Writer", "missing", "lint-kit"], firstMessage: "Start" },
    experts,
    skills,
  );
  eq([plan.roomId, plan.name, plan.expert?.id, plan.skills.map((s) => s.id), plan.firstMessage], ["q3-review", "Q3 评审", "e1", ["s1", "s2"], "Start"], "a plan");
  eq(roomPlanFrom({ roomId: "a/b" }, experts, skills).roomId, "a-b", "an invalid id is made a slug");
  eq(roomPlanFrom({ expert: "nobody" }, experts, skills).expert, null, "an unknown expert");
  eq(findByName(skills, ""), null, "an empty name finds nothing");
}

// draft-schedule: the recurrence fields, invalid parts dropped, the kind only when complete.
{
  eq(scheduleDraftFrom({ text: "Standup", kind: "weekly", time: "09:30", days: ["1", "2", "3", "4", "5", "9", "1"] }), { text: "Standup", form: { time: "09:30", days: [1, 2, 3, 4, 5], kind: "weekly" } }, "weekly");
  eq(scheduleDraftFrom({ kind: "daily", time: "25:00" }), { text: "", form: {} }, "a bad time keeps the form's kind");
  eq(scheduleDraftFrom({ kind: "cron", cron: "0  9 * * 1-5" }), { text: "", form: { cron: "0 9 * * 1-5", kind: "cron" } }, "cron");
  eq(scheduleDraftFrom({ kind: "cron", cron: "0 9 *" }), { text: "", form: {} }, "a short cron is dropped");
  eq(scheduleDraftFrom({ kind: "once", at: "2026-10-20T14:00" }), { text: "", form: { onceLocal: "2026-10-20T14:00", kind: "once" } }, "once");
  eq(scheduleDraftFrom({ kind: "hourly", time: "10:00" }), { text: "", form: { time: "10:00" } }, "an unknown kind");
}
