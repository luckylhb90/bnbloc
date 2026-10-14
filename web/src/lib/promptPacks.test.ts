import { formPayload, type ContentComponent, type ContentUse } from "./contentComponents.ts";
import {
  choiceFromRef,
  emptyPromptPackForm,
  fillPrompt,
  formFromPack,
  initialValues,
  insertPrompt,
  missingValue,
  packUses,
  placeholders,
  promptChoices,
  promptImportContentType,
  promptMatches,
  promptPackPayload,
  promptPackProblem,
  promptsFromMessages,
  fileSuggestions,
  starterChoices,
  slashMatches,
  slashQuery,
  slugify,
  syncVariables,
  titleFromText,
  uniqueId,
  variableLabel,
  type ContentPrompt,
} from "./promptPacks.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function prompt(id: string, over: Partial<ContentPrompt> = {}): ContentPrompt {
  return { id, title: id, body: `body of ${id}`, uses: 0, ...over };
}

function pack(id: string, prompts: ContentPrompt[], over: Partial<ContentComponent> = {}): ContentComponent {
  return {
    id,
    kind: "prompt",
    name: id,
    displayName: `Pack ${id}`,
    owner: "maya",
    scope: "personal",
    status: "active",
    latestVersion: "1.0.0",
    version: "1.0.0",
    permissions: [],
    requires: {},
    mine: true,
    acked: true,
    uses: 0,
    runs: 0,
    members: 0,
    createdAtMs: 1,
    updatedAtMs: 1,
    prompts,
    ...over,
  } as ContentComponent;
}

function use(c: ContentComponent, scope: "member" | "room" = "member"): ContentUse {
  return { scope, scopeId: "x", componentId: c.id, kind: c.kind, createdBy: "maya", createdAtMs: 1, component: c };
}

// ---- choices: member packs first, then the room's, each pack once.
const notes = prompt("notes", { slash: "/notes", uses: 2, tags: ["writing"] });
const nudge = prompt("nudge", { slash: "/nudge", uses: 5 });
const plain = prompt("plain", { title: "Summarize", body: "Summarize the thread" });
const mine = pack("writing", [notes, plain]);
const room = pack("team", [nudge]);
const expert = { ...pack("x", []), kind: "expert" } as ContentComponent;
const published = pack("office", [prompt("polish", { slash: "/polish" })]);
const choices = promptChoices([use(mine), use(expert)], [use(room, "room"), use(mine, "room")], [mine, published, expert]);
eq(
  choices.map((c) => `${c.pack}/${c.prompt.id}:${c.source}`),
  ["writing/notes:mine", "writing/plain:mine", "team/nudge:room", "office/polish:library"],
  "choices: turned-on packs first, then the rest of the Library, each pack once",
);
eq(choices[0].packLabel, "Pack writing", "pack label");

// ---- slash commands: only a message that starts with "/word".
eq(slashQuery("/no"), "no", "slash query");
eq(slashQuery("  /"), "", "bare slash");
eq(slashQuery("hello /no"), null, "slash mid-text");
eq(slashQuery("/no tes"), null, "slash with a space");
eq(slashQuery("/usr/bin"), null, "a path");
eq(slashMatches(choices, "n").map((c) => c.prompt.id), ["nudge", "notes"], "most used first");
eq(slashMatches(choices, "notes").map((c) => c.prompt.id), ["notes"], "exact");
eq(slashMatches(choices, "zz"), [], "no match");
eq(slashMatches(choices, "", 1).length, 1, "limit");

// ---- picker search.
eq(promptMatches(choices, "").map((c) => c.prompt.id), ["nudge", "notes", "plain", "polish"], "no query: most used");
eq(promptMatches(choices, "WRITING").map((c) => c.prompt.id), ["notes", "plain"], "pack label and tags match");
eq(promptMatches(choices, "thread").map((c) => c.prompt.id), ["plain"], "body matches");

// ---- variables.
eq(placeholders("Hi {{ name }}, {{name}} and {{tone}} but not {{a b}} or {{1x}}"), ["name", "tone"], "placeholders");
const vars = [{ name: "audience", default: "users" }, { name: "tone", options: ["formal", "casual"] }, { name: "topic" }];
eq(initialValues(vars), { audience: "users", tone: "formal", topic: "" }, "initial values");
eq(missingValue(vars, initialValues(vars))?.name, "topic", "missing value");
eq(missingValue(vars, { audience: "a", tone: "b", topic: "c" }), null, "all filled");
eq(fillPrompt("For {{audience}} in a {{ tone }} tone, {{audience}}; keep {{other}}", { audience: "devs", tone: "casual" }), "For devs in a casual tone, devs; keep {{other}}", "fill");
eq(variableLabel({ name: "target_audience" }), "Target audience", "label from name");
eq(variableLabel({ name: "x", label: " Who " }), "Who", "label");

// ---- insertion: a typed /command is replaced; a draft is kept.
eq(insertPrompt("/no", "Prompt", 3), "Prompt", "replaces the command");
eq(insertPrompt("/no and more", "Prompt", 3), "Prompt\n\nand more", "keeps text after the command");
eq(insertPrompt("", "Prompt", null), "Prompt", "empty draft");
eq(insertPrompt("draft  \n", "Prompt", null), "draft\n\nPrompt", "appended to a draft");

// ---- quick prompt references become choices.
const ref = choiceFromRef({ componentId: "cc_9", pack: "writing", version: "1.2.0", prompt: notes });
eq([ref.componentId, ref.version, ref.prompt.id], ["cc_9", "1.2.0", "notes"], "choice from ref");

// ---- the Library form.
eq(slugify("  Release Notes: v2!  "), "release-notes-v2", "slugify");
eq(uniqueId("Release notes", new Set(["release-notes", "release-notes-2"])), "release-notes-3", "unique id");
eq(uniqueId("!!!", new Set()), "prompt", "id fallback");
eq(titleFromText("\n## Weekly report\nmore"), "Weekly report", "title from text");
eq(
  syncVariables("{{b}} then {{a}}", [{ name: "a", label: "A", default: "", options: "" }, { name: "gone", label: "", default: "", options: "" }]).map((v) => v.name + ":" + v.label),
  ["b:", "a:A"],
  "variables follow the body",
);

const form = emptyPromptPackForm();
eq(promptPackProblem(form), "Give the pack a name", "needs a name");
form.name = "Writing";
eq(promptPackProblem(form), "The name uses lower-case letters, digits and hyphens", "bad name");
form.name = "writing";
eq(promptPackProblem(form), "Add at least one prompt", "needs a prompt");
form.prompts = [
  { id: "", title: "Notes", body: "Notes for {{audience}}", slash: "/Notes", tags: "Writing, ops", variables: [{ name: "audience", label: "Audience", default: "devs", options: "users\ndevs\n" }], starter: true, files: "" },
  { id: "", title: "", body: "Second prompt\nline", slash: "", tags: "", variables: [], starter: false, files: "" },
  { id: "", title: "", body: "", slash: "", tags: "", variables: [], starter: false, files: "" },
];
eq(promptPackProblem(form), null, "valid");
eq(
  promptPackPayload(form),
  {
    kind: "prompt",
    name: "writing",
    prompts: [
      { id: "notes", title: "Notes", body: "Notes for {{audience}}", slash: "/notes", tags: ["writing", "ops"], variables: [{ name: "audience", label: "Audience", default: "devs", options: ["users", "devs"] }], starter: true },
      { id: "second-prompt", title: "Second prompt", body: "Second prompt\nline" },
    ],
  },
  "payload",
);
form.prompts[1].slash = "notes";
eq(promptPackProblem(form), "Two prompts use /notes", "duplicate slash");
form.prompts[1].slash = "bad slash";
eq(promptPackProblem(form), "The command /bad slash uses lower-case letters, digits and hyphens", "bad slash");
form.prompts[1].slash = "";
form.prompts[0].variables[0].default = "nobody";
eq(promptPackProblem(form), "The default of audience is not one of its options", "default not an option");

// Round trip: a pack prefills the form, its payload keeps the prompts.
const back = promptPackPayload(formFromPack(pack("writing", [prompt("a", { body: "Hi {{who}}", variables: [{ name: "who", options: ["x", "y"] }], slash: "/a" })])));
eq((back.prompts as unknown[]).length, 1, "round trip count");
eq((back.prompts as { slash: string; variables: unknown }[])[0], { id: "a", title: "a", body: "Hi {{who}}", slash: "/a", variables: [{ name: "who", options: ["x", "y"] }] }, "round trip");

// "From selected messages": one prompt per message, unique ids.
eq(
  promptsFromMessages(["Review {{file}} carefully", "  ", "Review {{file}} carefully"], [{ ...form.prompts[0], id: "review-file-carefully" }]).map((d) => [d.id, d.title, d.variables.map((v) => v.name)]),
  [
    ["review-file-carefully-2", "Review {{file}} carefully", ["file"]],
    ["review-file-carefully-3", "Review {{file}} carefully", ["file"]],
  ],
  "from messages",
);

eq(packUses(pack("p", [prompt("a", { uses: 2 }), prompt("b", { uses: 3 })])), 5, "pack uses");
eq(promptImportContentType("Prompts.MD"), "text/markdown", "md upload");
eq(promptImportContentType("p.json"), "application/json", "json upload");
eq(promptImportContentType("p.tgz"), "application/gzip", "archive upload");

// An expert form keeps a quick prompt that points at a pack's prompt.
eq(
  formPayload({
    kind: "expert",
    name: "writer",
    displayName: "",
    description: "",
    category: "",
    instructions: "Write.",
    welcome: "",
    quickPrompts: [{ title: "", prompt: "", ref: "writing/notes" }, { title: "T", prompt: "text" }, { title: "", prompt: "" }],
    skills: [],
    requires: { prompts: ["writing@^1"] },
  }).quickPrompts,
  [{ title: "", ref: "writing/notes" }, { title: "T", prompt: "text" }],
  "quick prompt refs survive the form",
);

// ---- file suggestions (docs/prd/2026-10-14-file-prompts-and-compare.md rule 2).
{
  const deck = { name: "q3.pptx", mimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", extension: "pptx" };
  const office = { extensions: [".pptx", ".docx", ".xlsx"] };
  const builtin = pack("file-prompts", [
    prompt("tighten", { title: "Tighten the wording", starter: true, files: { extensions: [".pptx"] } }),
    prompt("summary", { title: "Summarize", starter: true, files: office }),
    prompt("not-starter", { files: { extensions: [".pptx"] } }),
    prompt("image", { title: "Describe it", starter: true, files: { mimeTypes: ["image/*"] } }),
  ], { scope: "builtin" });
  const org = pack("finance", [prompt("org-summary", { title: "summarize", starter: true, files: { extensions: [".pptx"] }, uses: 3 })]);
  const roomPack = pack("room-kit", [prompt("kickoff", { title: "Kick off", starter: true })]);
  const choices = promptChoices([], [use(roomPack, "room")], [org, builtin]);
  eq(
    fileSuggestions(choices, deck).map((c) => [c.pack, c.prompt.id]),
    [
      ["finance", "org-summary"],
      ["file-prompts", "tighten"],
    ],
    "the most specific first, the Library before built-in, one per title, starters only",
  );
  eq(fileSuggestions(choices, { name: "a.webp", mimeType: "image/webp", extension: "webp" }).map((c) => c.prompt.id), ["image"], "a wildcard match");
  eq(fileSuggestions(choices, { name: "notes.txt", mimeType: "text/plain", extension: "txt" }), [], "no match, no suggestions");
  // File suggestions are never room starters.
  eq(starterChoices([use(roomPack, "room")], [builtin]).map((c) => c.prompt.id), ["kickoff"], "room starters skip file prompts");
  // The scope round-trips through the Library form.
  const back = promptPackPayload(formFromPack(builtin));
  eq((back.prompts as { files?: unknown }[])[0].files, { extensions: [".pptx"] }, "files round trip");
  eq((back.prompts as { files?: unknown }[])[3].files, { mimeTypes: ["image/*"] }, "mime round trip");
}

console.log("promptPacks tests passed");

// ---- starters: the prompts flagged starter of the room's prompt packs, each pack once, bounded.
{
  const starter = prompt("kickoff", { title: "Kick off", starter: true });
  const other = prompt("other");
  const roomPack = pack("room-kit", [other, starter]);
  const skills = { ...pack("skills", [prompt("x", { starter: true })]), kind: "skill" as const };
  const got = starterChoices([use(skills, "room"), use(roomPack, "room"), use(roomPack, "room")], []);
  eq(got.map((c) => [c.pack, c.prompt.id, c.source]), [["room-kit", "kickoff", "room"]], "room starters");
  const many = pack("many", Array.from({ length: 9 }, (_, i) => prompt(`p${i}`, { starter: true })));
  eq(starterChoices([use(many, "room")], []).length, 6, "starters are bounded");
  // The flag round-trips through the form.
  const back = promptPackPayload(formFromPack(pack("kit", [starter, other])));
  eq((back.prompts as { starter?: boolean }[]).map((p) => !!p.starter), [true, false], "starter round trip");
}
