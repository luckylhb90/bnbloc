import { cleanTags, emptyContentForm, formFromVersion, formPayload, formProblem, isTeam, splitList, usageLine, type ContentForm } from "./contentComponents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// The Library form edits tags, icon, requirements and a team's roster.
const form: ContentForm = {
  ...emptyContentForm("expert"),
  name: "release-team",
  instructions: "Plan, delegate, merge.",
  tags: ["Release", " review ", "release"],
  icon: "rocket",
  requires: { skills: ["lint@^1"], prompts: ["notes"], permissions: ["scripts"] },
  team: {
    lead: "coordinator",
    members: [
      { role: "reviewer", expert: "code-reviewer@^1", defaultAgent: "member:jack/codex", description: "Reviews the diff." },
      { role: "", expert: "", defaultAgent: "requester", description: "" },
    ],
  },
};
eq(formProblem(form), null, "valid team form");
const payload = formPayload(form);
eq(payload.tags, ["release", "review"], "tags cleaned");
eq(payload.icon, "rocket", "icon");
eq(payload.requires, { skills: ["lint@^1"], prompts: ["notes"], permissions: ["scripts"] }, "requirements");
eq(
  payload.team,
  { lead: "coordinator", members: [{ role: "reviewer", expert: "code-reviewer@^1", defaultAgent: "member:jack/codex", description: "Reviews the diff." }] },
  "team roster (blank rows dropped)",
);
eq(formPayload({ ...form, team: { lead: "", members: [] } }).team, undefined, "no roster, no team");

const bad = (patch: Partial<ContentForm>) => formProblem({ ...form, ...patch });
assert(bad({ tags: ["no!"] })?.includes("tag") ?? false, "bad tag refused");
assert(bad({ requires: { skills: ["Bad Name"] } })?.includes("requirement") ?? false, "bad requirement refused");
assert(bad({ team: { lead: "", members: [{ role: "A", expert: "x", defaultAgent: "platform", description: "" }] } })?.includes("role") ?? false, "bad role refused");
assert(
  bad({ team: { lead: "", members: [{ role: "a", expert: "x", defaultAgent: "platform", description: "" }, { role: "a", expert: "y", defaultAgent: "platform", description: "" }] } })?.includes("twice") ?? false,
  "duplicate role refused",
);
assert(bad({ team: { lead: "", members: [{ role: "a", expert: "x", defaultAgent: "anyone", description: "" }] } })?.includes("agent") ?? false, "bad agent refused");

// A version prefills the form, roster included (the default lead title stays empty).
const back = formFromVersion({
  component: {
    id: "cc_t", kind: "expert", name: "release-team", owner: "maya", scope: "personal", status: "active", latestVersion: "1.0.0", version: "1.0.0",
    permissions: [], requires: {}, mine: true, acked: true, uses: 0, createdAtMs: 0, updatedAtMs: 0, tags: ["release"], icon: "rocket",
    team: { lead: "lead", members: [{ role: "reviewer", expert: "code-reviewer@^1", defaultAgent: "requester" }] },
  },
  version: { version: "1.0.0", sha256: "", bundleKey: "", bundleUrl: "", sizeBytes: 0, createdBy: "maya", createdAtMs: 0 },
  instructions: "SOP",
  files: [],
});
eq(back.tags, ["release"], "tags back");
eq(back.icon, "rocket", "icon back");
eq(back.team, { lead: "", members: [{ role: "reviewer", expert: "code-reviewer@^1", defaultAgent: "requester", description: "" }] }, "team back");

eq(cleanTags(["A", "a", " "]), ["a"], "clean tags");
eq(splitList("a, b\nc,,"), ["a", "b", "c"], "split list");
eq(isTeam({ team: { lead: "lead", members: [] } }), false, "empty roster is not a team");
eq(isTeam({ team: { lead: "lead", members: [{ role: "a", expert: "x", defaultAgent: "platform" }] } }), true, "team");
eq(usageLine({ kind: "expert", runs: 12, members: 1 }), "12 runs · 1 member", "expert usage");
eq(usageLine({ kind: "skill", runs: 0, members: 4 }), "4 members", "pack usage");
eq(usageLine({ kind: "expert", runs: 9999, members: 9999 }), "9999+ runs · 9999+ members", "capped usage");
eq(usageLine({ kind: "expert" }), "", "unused");
