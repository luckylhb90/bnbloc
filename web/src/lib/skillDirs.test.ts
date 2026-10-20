import type { ComponentSettingField } from "./componentSettings.ts";
import { candidateField, suggestionsFrom, suggestRequestText, withFolders } from "./skillDirs.ts";
import { MaxComponentSettingLines } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const candidates = [
  { path: "~/.newtool/skills", skills: 2, sample: "deploy" },
  { path: "~/.cache/old/skills", skills: 1, sample: "lint" },
];

// The assist is asked with the folders in use and the candidates: names
// and counts only.
eq(
  suggestRequestText(["~/.agents/skills"], candidates),
  "Folders already used:\n- ~/.agents/skills\n\nCandidates:\n- ~/.newtool/skills (2 skills, such as deploy)\n- ~/.cache/old/skills (1 skill, such as lint)",
  "the request",
);
assert(suggestRequestText([], candidates).startsWith("Folders already used:\n- none\n"), "none in use");

// Only listed candidates are proposed, each once.
eq(
  suggestionsFrom(candidates, {
    add: [
      { path: "~/.newtool/skills", why: "Newtool reads skills here." },
      { path: "~/.newtool/skills", why: "again" },
      { path: "/etc/invented", why: "made up" },
      { path: "~/.cache/old/skills" },
    ],
    notes: "None left out.",
  }),
  { why: { "~/.newtool/skills": "Newtool reads skills here.", "~/.cache/old/skills": "Proposed." }, notes: "None left out." },
  "suggestions",
);
eq(suggestionsFrom(candidates, {}), { why: {}, notes: "" }, "an empty reply");

// The setting they go into is the provider's member lines setting.
const dirs: ComponentSettingField = { key: "extraSkillDirs", label: "Extra folders", type: "lines", scope: "member", set: true, value: "~/.a/skills" };
eq(candidateField("extraSkillDirs", [dirs])?.key, "extraSkillDirs", "found");
eq(candidateField("extraSkillDirs", [{ ...dirs, type: "text" }]), null, "not a list");
eq(candidateField("extraSkillDirs", [{ ...dirs, scope: "org" }]), null, "not the member's");
eq(candidateField("other", [dirs]), null, "not declared");
eq(candidateField(undefined, [dirs]), null, "no candidates");

// Ticked folders are added after the setting's own lines, each once, within
// the bounds.
eq(withFolders("~/.a/skills\n~/.b/skills", ["~/.b/skills", "~/.c/skills"]), { value: "~/.a/skills\n~/.b/skills\n~/.c/skills" }, "added once");
eq(withFolders(undefined, ["~/.c/skills"]), { value: "~/.c/skills" }, "a first value");
const full = Array.from({ length: MaxComponentSettingLines }, (_, i) => `~/.t${i}/skills`).join("\n");
eq(withFolders(full, ["~/.more/skills"]), { why: `at most ${MaxComponentSettingLines} lines (this has ${MaxComponentSettingLines + 1})` }, "over the bound");
