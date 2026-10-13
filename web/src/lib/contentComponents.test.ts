import {
  archiveContentType,
  chatExpert,
  compareVersions,
  emptyContentForm,
  expertBadge,
  expertChoices,
  expertMarker,
  expertMentionMatches,
  formFromVersion,
  formPayload,
  formProblem,
  matchesContent,
  needsConfirmation,
  permissionLabel,
  updateAvailable,
  type ContentComponent,
  type ContentUse,
} from "./contentComponents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

function comp(over: Partial<ContentComponent>): ContentComponent {
  return {
    id: "cc_1",
    kind: "expert",
    name: "reviewer",
    displayName: "Reviewer",
    owner: "maya",
    scope: "personal",
    status: "active",
    latestVersion: "1.2.0",
    version: "1.2.0",
    permissions: [],
    requires: {},
    mine: true,
    removable: false,
    acked: true,
    runs: 0,
    members: 0,
    uses: 0,
    favorite: false,
    favoriteCount: 0,
    createdAtMs: 1,
    updatedAtMs: 1,
    ...over,
  };
}

// Versions order by semver, prerelease below release; a pin behind latest
// has an update.
eq(compareVersions("1.10.0", "1.9.3"), 1, "minor two digits");
eq(compareVersions("1.0.0-rc.1", "1.0.0"), -1, "prerelease below release");
eq(compareVersions("2.0.0", "2.0.0"), 0, "equal");
assert(updateAvailable("1.0.0", "1.0.1"), "pinned behind latest");
assert(!updateAvailable("1.0.1", "1.0.1"), "pinned at latest");
assert(!updateAvailable(undefined, "3.0.0"), "following latest");

// The form payload drops blanks; experts send instructions, quick prompts
// and welcome; skill packs send skills only.
{
  const f = emptyContentForm("expert");
  f.name = " reviewer ";
  f.displayName = "Reviewer";
  f.instructions = "  Be strict.  ";
  f.quickPrompts = [{ title: "Go", prompt: "Review it" }, { title: " ", prompt: "" }];
  f.skills = [{ name: "checklist", description: "Use when reviewing", body: "1. tests\n" }, { name: "", description: "", body: "" }];
  f.requires = { permissions: ["scripts"] };
  eq(
    formPayload(f),
    {
      kind: "expert",
      name: "reviewer",
      displayName: "Reviewer",
      requires: { permissions: ["scripts"] },
      skills: [{ name: "checklist", description: "Use when reviewing", body: "1. tests" }],
      instructions: "Be strict.",
      quickPrompts: [{ title: "Go", prompt: "Review it" }],
    },
    "expert payload",
  );
  const s = emptyContentForm("skill");
  s.name = "kit";
  s.instructions = "ignored";
  s.skills = [{ name: "a", description: "does a", body: "a" }];
  eq(formPayload(s), { kind: "skill", name: "kit", skills: [{ name: "a", description: "does a", body: "a" }] }, "skill payload");
}

// An AI assist's form: its instructions, output, fields (JSON only) and context.
{
  const a = emptyContentForm("assist");
  a.name = "describe-work";
  a.instructions = " Describe it. ";
  eq(formProblem(a), null, "a text assist with instructions");
  a.assist = { output: "json", fields: [], context: ["artifact"] };
  eq(formProblem(a), "A JSON assist names the fields of its reply", "a JSON assist needs fields");
  a.assist = { output: "json", fields: [{ name: "tag list", description: "x" }], context: ["artifact"] };
  eq(formProblem(a), "Each field name uses letters, digits and _ (starting with a letter)", "a field name");
  a.assist = { output: "json", fields: [{ name: " tags ", description: " Tags ", list: true }, { name: "", description: "" }], context: ["artifact"] };
  eq(
    formPayload(a),
    { kind: "assist", name: "describe-work", instructions: "Describe it.", output: "json", fields: [{ name: "tags", description: "Tags", list: true }], context: ["artifact"] },
    "assist payload",
  );
  a.assist = { output: "text", fields: [{ name: "left", description: "" }], context: [] };
  eq(formPayload(a), { kind: "assist", name: "describe-work", instructions: "Describe it.", output: "text" }, "a text assist sends no fields");
  a.instructions = "";
  eq(formProblem(a), "An AI assist needs instructions", "an assist needs instructions");
}

// Early form checks.
{
  const f = emptyContentForm("expert");
  eq(formProblem(f), "Give it a name", "no name");
  f.name = "Reviewer";
  eq(formProblem(f), "The name uses lower-case letters, digits and hyphens", "bad name");
  f.name = "reviewer";
  eq(formProblem(f), "An expert needs instructions", "no instructions");
  f.instructions = "x";
  eq(formProblem(f), null, "valid expert");
  f.skills = [{ name: "lint", description: "", body: "x" }];
  eq(formProblem(f), "Describe when to use the skill lint", "skill needs a description");
  const s = emptyContentForm("skill");
  s.name = "kit";
  eq(formProblem(s), "A skill pack needs at least one skill", "empty pack");
}

// A version detail prefills the form, skill bodies included.
eq(
  formFromVersion({
    component: comp({ skills: [{ name: "checklist", description: "d" }], quickPrompts: [{ title: "t", prompt: "p" }], welcome: "hi", requires: { skills: ["x@^1"] } }),
    version: { version: "1.2.0", sha256: "s", bundleKey: "k", bundleUrl: "/u", sizeBytes: 1, createdBy: "maya", createdAtMs: 1 },
    instructions: "\nBe strict.\n",
    skillBodies: { checklist: "\nbody\n" },
    files: [],
  }),
  {
    kind: "expert",
    name: "reviewer",
    displayName: "Reviewer",
    description: "",
    category: "",
    instructions: "Be strict.",
    welcome: "hi",
    quickPrompts: [{ title: "t", prompt: "p" }],
    skills: [{ name: "checklist", description: "d", body: "body" }],
    tags: [],
    icon: "",
    requires: { skills: ["x@^1"] },
    team: { lead: "", members: [] },
  },
  "form from version",
);

// Permissions confirm only for someone else's unconfirmed declared ones.
assert(!needsConfirmation(comp({ permissions: ["scripts"] })), "own component");
assert(needsConfirmation(comp({ mine: false, acked: false, permissions: ["scripts"] })), "someone else's");
assert(!needsConfirmation(comp({ mine: false, acked: false })), "nothing declared");
eq(permissionLabel("mcp.http:tools.example.com"), "Calls tools.example.com", "http permission");

// Search spans names, description and skills.
assert(matchesContent(comp({ skills: [{ name: "lint-rules", description: "" }] }), "LINT"), "skill name");
assert(!matchesContent(comp({}), "deploy"), "no match");

// Composer choices: the room's pins first (with their version), then mine,
// one per component; only experts.
{
  const mine = [comp({ id: "a", name: "reviewer" }), comp({ id: "b", name: "writer", displayName: "Writer" }), comp({ id: "k", kind: "skill", name: "kit" })];
  const room: ContentUse[] = [
    { scope: "room", scopeId: "r1", componentId: "a", kind: "expert", pinnedVersion: "1.0.0", createdBy: "maya", createdAtMs: 1, component: comp({ id: "a", version: "1.0.0" }) },
    { scope: "room", scopeId: "r1", componentId: "z", kind: "expert", createdBy: "jack", createdAtMs: 2, component: comp({ id: "z", name: "planner", displayName: "Planner", mine: false }) },
  ];
  const choices = expertChoices(mine, room);
  eq(choices.map((c) => `${c.component.id}:${c.source}:${c.version ?? ""}`), ["a:room:1.0.0", "z:room:", "b:mine:"], "choices");
  eq(chatExpert(choices[0]), { id: "a", version: "1.0.0" }, "pinned send");
  eq(chatExpert(choices[2]), { id: "b" }, "latest send");
  eq(expertMentionMatches(choices, "pl").map((c) => c.component.id), ["z"], "mention by name");
  eq(expertMentionMatches(choices, "rev").map((c) => c.component.id), ["a"], "mention by label");
  eq(expertMentionMatches(choices, "maya/co"), [], "agent handle query");
}

// The chat payload marker and its badge.
eq(expertMarker({ expert: { id: "cc_1", name: "reviewer", displayName: "Reviewer", version: "1.2.0" } }), { id: "cc_1", name: "reviewer", displayName: "Reviewer", version: "1.2.0" }, "marker");
eq(
  expertMarker({ expert: { id: "cd_1", name: "reviewer", displayName: "Reviewer", version: "1.0.0", draft: true } }),
  { id: "cd_1", name: "reviewer", displayName: "Reviewer", version: "1.0.0", draft: true },
  "a quick try's draft marker",
);
eq(expertMarker({ text: "x" }), undefined, "no marker");
eq(expertMarker({ expert: { name: "x" } }), undefined, "marker without id");
eq(expertBadge({ name: "reviewer", displayName: "Reviewer", version: "1.2.0" }), "Reviewer v1.2.0", "badge");
eq(expertBadge({ name: "reviewer" }), "reviewer", "badge without version");

// Import uploads name their archive format.
eq(archiveContentType("skills.ZIP"), "application/zip", "zip");
eq(archiveContentType("kit.tar.gz"), "application/gzip", "tar.gz");
eq(archiveContentType("kit.tgz"), "application/gzip", "tgz");
eq(archiveContentType("kit.tar"), "application/x-tar", "tar");
