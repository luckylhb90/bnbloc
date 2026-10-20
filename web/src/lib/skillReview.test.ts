import { tarArchive } from "./contentDrafts.ts";
import { skillReviewFrom, skillReviewText, tarFiles } from "./skillReview.ts";
import { MaxAssistText } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const enc = new TextEncoder();
const dec = new TextDecoder();
const md = "---\nname: lint-rules\ndescription: Lints\n---\nRun /Users/maya/bin/lint.sh on the change.\n";
const deep = `lint-rules/${"nested/".repeat(16)}rules.yml`;

// A packed skill (ustar, a long path split into its prefix) reads back.
{
  const archive = tarArchive([
    { path: "lint-rules/SKILL.md", data: enc.encode(md) },
    { path: "lint-rules/scripts/run.sh", data: enc.encode("echo lint\n") },
    { path: deep, data: new Uint8Array(0) },
  ]);
  const files = tarFiles(archive);
  eq(files.map((f) => f.path), ["lint-rules/SKILL.md", deep, "lint-rules/scripts/run.sh"], "paths");
  eq(dec.decode(files[0]!.data), md, "content");
  let threw = false;
  try {
    tarFiles(archive.subarray(0, 520));
  } catch {
    threw = true;
  }
  assert(threw, "a cut archive is malformed");

  // The review is asked with the folder, its files and SKILL.md only.
  const asked = skillReviewText("lint-rules", files);
  eq(asked.split("\n").slice(0, 5), ["Skill folder: lint-rules", "Files (3):", "- SKILL.md", `- ${deep.slice("lint-rules/".length)}`, "- scripts/run.sh"], "the head");
  assert(asked.endsWith(`SKILL.md:\n${md}`), "SKILL.md follows");
  assert(!asked.includes("echo lint"), "no other file's content");
  const long = skillReviewText("big", [{ path: "big/SKILL.md", data: enc.encode("x".repeat(20000)) }]);
  eq([long.length, long.endsWith("…")], [MaxAssistText, true], "cut to the request's bound");
  assert(skillReviewText("bare", []).endsWith("SKILL.md:\n(none)"), "no SKILL.md says so");
}

// A reply becomes findings and an entry the library takes.
{
  const r = skillReviewFrom({
    findings: [" SKILL.md line 5 names /Users/maya: use a path relative to the skill. ", "", "The description does not say when to use it."],
    name: "House Lint Rules!",
    displayName: "House  lint\nrules",
    description: "Use when a change needs the house lint rules.",
    tags: ["Lint", "lint", "code review", "#bad"],
  });
  eq(r.findings, ["SKILL.md line 5 names /Users/maya: use a path relative to the skill.", "The description does not say when to use it."], "findings");
  eq(r.proposed, { name: "house-lint-rules", displayName: "House lint rules", description: "Use when a change needs the house lint rules.", tags: ["lint", "code review"] }, "entry");
  eq(skillReviewFrom({ findings: [] }), { findings: [], proposed: {} }, "nothing to fix, nothing proposed");
  eq(skillReviewFrom({ findings: "One thing, then another." }).findings, ["One thing, then another."], "one finding as a string");
  eq(skillReviewFrom({ findings: Array.from({ length: 9 }, (_, i) => `f${i}`) }).findings.length, 6, "at most six");
}
