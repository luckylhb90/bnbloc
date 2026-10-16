import {
  formatBytes,
  publishLabel,
  reachCounts,
  skillLine,
  unframePackedSkill,
  withAllowed,
  withChoice,
  withEnabled,
  withoutAllowed,
  withoutSource,
  withPublished,
  withSource,
  type SkillRow,
  type SkillsSettings,
} from "./localSkills.ts";

function eq<T>(got: T, want: T, msg: string): void {
  if (JSON.stringify(got) !== JSON.stringify(want)) throw new Error(`${msg}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

const empty: SkillsSettings = { enabled: false, sources: [], agentDirs: [], skills: {} };

eq(withEnabled(empty, true).enabled, true, "turned on");
eq(withSource(empty, " ~/.agents/skills ").sources, ["~/.agents/skills"], "a source is added trimmed");
eq(withSource(withSource(empty, "/a"), "/a").sources, ["/a"], "a source is added once");
eq(withSource(empty, "  ").sources, [], "a blank folder is nothing");
eq(withoutSource(withSource(empty, "/a"), "/a").sources, [], "a source is removed");

const ticked = withChoice(empty, "alpha", { vm: true });
eq(ticked.skills, { alpha: { vm: true, borrowers: false } }, "ticked for my sandbox");
eq(withChoice(ticked, "alpha", { borrowers: true }).skills.alpha, { vm: true, borrowers: true }, "and for borrowers");
eq(withChoice(empty, "alpha", { borrowers: true }).skills, {}, "borrowers without my sandbox is nothing");
eq(withChoice(withChoice(ticked, "alpha", { borrowers: true }), "alpha", { vm: false }).skills, {}, "out of my sandbox takes it from borrowers and forgets the choice");

const published = withPublished(withChoice(ticked, "alpha", { vm: false }), "alpha", { hash: "h", componentId: "c1", version: "1.0.0" });
eq(published.skills.alpha, { vm: false, borrowers: false, published: { hash: "h", componentId: "c1", version: "1.0.0" } }, "a published skill keeps its record when unticked");
eq(withChoice(published, "alpha", { vm: true }).skills.alpha?.published?.hash, "h", "ticking keeps the publish record");
eq(empty.skills, {}, "settings are never edited in place");

const allowed = withAllowed(empty, "alpha", { file: "notes.md", hash: "h" });
eq(allowed.skills.alpha, { vm: false, borrowers: false, allowed: [{ file: "notes.md", hash: "h" }] }, "a file allowed");
eq(withAllowed(allowed, "alpha", { file: "notes.md", hash: "h2" }).skills.alpha?.allowed, [{ file: "notes.md", hash: "h2" }], "allowing again replaces the hash");
eq(withoutAllowed(allowed, "alpha", "notes.md").skills, {}, "stop allowing forgets an empty choice");
eq(withoutAllowed(withChoice(allowed, "alpha", { vm: true }), "alpha", "notes.md").skills.alpha, { vm: true, borrowers: false, allowed: undefined }, "but keeps a ticked one");

const row = (patch: Partial<SkillRow>): SkillRow => ({
  name: "a", description: "Does a", source: "/s", hasScripts: false, files: 1, bytes: 10, eligible: true, choice: { vm: false, borrowers: false }, ...patch,
});
eq(
  reachCounts([row({ choice: { vm: true, borrowers: true } }), row({ choice: { vm: true, borrowers: false } }), row({ eligible: false, choice: { vm: true, borrowers: true } }), row({ hiddenBy: "/x", choice: { vm: true, borrowers: true } })]),
  { own: 2, borrowers: 1 },
  "only usable, unhidden skills count",
);
eq(skillLine(row({ hasScripts: true })), "Does a · runs scripts", "a skill line");
eq(skillLine(row({ eligible: false, reason: "contains .env" })), "Not shared: contains .env", "why not");
eq(skillLine(row({ hiddenBy: "/x" })), "Hidden: /x has a skill of the same name.", "hidden");
eq(publishLabel(row({})), "Add to my library", "not published");
eq(publishLabel(row({ choice: { vm: false, borrowers: false, published: { hash: "h", componentId: "c", version: "1" } } })), "In your library", "published");
eq(publishLabel(row({ changed: true, choice: { vm: false, borrowers: false, published: { hash: "h", componentId: "c", version: "1" } } })), "Publish changes", "changed");
eq(publishLabel(row({ publishProblem: "My Notes.md: names" })), "Cannot add to library", "a name the library refuses");
eq(skillLine(row({ publishProblem: "My Notes.md: names" })), "Does a · not for the library: My Notes.md: names", "and why");
eq([formatBytes(10), formatBytes(2048), formatBytes(3 * 1024 * 1024)], ["10 B", "2.0 KB", "3.0 MB"], "sizes");

const framed = new Uint8Array([0x61, 0x62, 0x0a, 0x1f, 0x8b, 0x0a, 0x00]);
const un = unframePackedSkill(framed);
eq([un.hash, Array.from(un.archive)], ["ab", [0x1f, 0x8b, 0x0a, 0x00]], "the archive may hold line feeds");
let threw = false;
try {
  unframePackedSkill(new Uint8Array([1, 2]));
} catch {
  threw = true;
}
eq(threw, true, "an unframed answer is an error");
