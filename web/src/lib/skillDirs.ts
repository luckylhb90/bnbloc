import { fieldItems, fieldText } from "./assistDrafts.ts";
import { isLines, linesProblem, linesValue, parseLines, type ComponentSettingField } from "./componentSettings.ts";
import type { SkillFolderCandidate } from "./localSkills.ts";
import { MaxAssistText, type AssistJSONReply } from "./wire.gen.ts";

// Other tools' skill folders in the skills panel, the pure side: a skills
// provider offers candidates (folders under the home holding skills, with a
// count and one name; never a file's content) and names the member lines
// setting of its own they go into. "Suggest folders with AI" asks the
// suggest-skill-dirs assist which to add and why; the member ticks them,
// and the ticked ones are added to that setting's lines, held to the same
// helpers as a typed value and saved through the member's settings store.
// Without an assist model the candidates are still listed for ticking.
// Unit-tested.

/** The built-in assist that proposes skill folders. */
export const SUGGEST_SKILL_DIRS = "suggest-skill-dirs";

const MAX_LISTED = 40;

function folderLine(c: SkillFolderCandidate): string {
  return `- ${c.path} (${c.skills} skill${c.skills === 1 ? "" : "s"}, such as ${c.sample})`;
}

/** suggestRequestText is suggest-skill-dirs' text: the folders the member
 *  already uses, then the candidates (at most 40 each, cut to the bound). */
export function suggestRequestText(using: readonly string[], candidates: readonly SkillFolderCandidate[]): string {
  const used = using.slice(0, MAX_LISTED).map((p) => `- ${p}`);
  const text = `Folders already used:\n${used.length ? used.join("\n") : "- none"}\n\nCandidates:\n${candidates.slice(0, MAX_LISTED).map(folderLine).join("\n")}`;
  return text.slice(0, MaxAssistText);
}

/** suggestionsFrom is a suggest-skill-dirs reply: why, by path, for the
 *  listed candidates it proposes (any other path is dropped), and its notes. */
export function suggestionsFrom(candidates: readonly SkillFolderCandidate[], reply: AssistJSONReply["fields"]): { readonly why: Readonly<Record<string, string>>; readonly notes: string } {
  const why: Record<string, string> = {};
  for (const item of fieldItems(reply, "add")) {
    const path = item.path ?? "";
    if (candidates.some((c) => c.path === path) && !(path in why)) why[path] = item.why || "Proposed.";
  }
  return { why, notes: fieldText(reply, "notes") };
}

/** candidateField is the provider's setting the candidates go into (the
 *  key its state names): one of its member lines settings, else null (the
 *  panel then offers no ticking). */
export function candidateField(setting: string | undefined, fields: readonly ComponentSettingField[]): ComponentSettingField | null {
  const f = setting ? fields.find((x) => x.key === setting) : undefined;
  return f && f.scope === "member" && isLines(f) ? f : null;
}

/** withFolders is the setting's value with the ticked folders added after
 *  its own lines (each once), or why it cannot hold them. */
export function withFolders(current: string | undefined, add: readonly string[]): { readonly value: string } | { readonly why: string } {
  const lines = parseLines(linesValue([current ?? "", ...add]));
  const unique = lines.filter((l, i) => lines.indexOf(l) === i);
  const why = linesProblem(unique);
  return why ? { why } : { value: unique.join("\n") };
}
