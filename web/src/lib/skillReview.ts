import { fieldList, fieldText } from "./assistDrafts.ts";
import { cleanTags, tagProblem } from "./contentComponents.ts";
import { slugify } from "./promptPacks.ts";
import { MaxAssistText, type AssistJSONReply } from "./wire.gen.ts";

// Publishing a local skill to the library, its pure side: the files of the
// packed skill (its provider's tar), the text the review-skill assist is
// asked with (the folder's name, its file list and its SKILL.md), and the
// reply as findings and an entry the member applies field by field. The
// publish stays the member's (views/skills/SkillPublishDialog.tsx); the
// content validator owns what the entry may be. Unit-tested.

/** The built-in assist that reviews a skill before it is published. */
export const REVIEW_SKILL = "review-skill";

export interface TarFile {
  readonly path: string;
  readonly data: Uint8Array;
}

const BLOCK = 512;
const text = new TextDecoder();

function field(h: Uint8Array, offset: number, size: number): string {
  const raw = h.subarray(offset, offset + size);
  const end = raw.indexOf(0);
  return text.decode(end < 0 ? raw : raw.subarray(0, end));
}

/** tarFiles reads the regular files of a ustar archive (a skills provider
 *  packs one in the library's bundle format: no extensions): each header's
 *  name under its prefix, its octal size, its data. A directory or link
 *  is skipped; a malformed archive throws. */
export function tarFiles(archive: Uint8Array): TarFile[] {
  const out: TarFile[] = [];
  for (let at = 0; at + BLOCK <= archive.length; ) {
    const h = archive.subarray(at, at + BLOCK);
    if (h.every((b) => b === 0)) break;
    const size = parseInt(field(h, 124, 12).trim() || "0", 8);
    if (!Number.isSafeInteger(size) || size < 0 || at + BLOCK + size > archive.length) throw new Error("the packed skill is malformed");
    const name = field(h, 0, 100);
    const prefix = field(h, 257, 5) === "ustar" ? field(h, 345, 155) : "";
    const type = h[156];
    if (type === 0x30 || type === 0) out.push({ path: prefix ? `${prefix}/${name}` : name, data: archive.subarray(at + BLOCK, at + BLOCK + size) });
    at += BLOCK + Math.ceil(size / BLOCK) * BLOCK;
  }
  return out;
}

const MAX_LISTED = 200;
const SKILL_MD = "SKILL.md";

/** skillReviewText is what review-skill is asked with: the folder's name,
 *  its files (relative to it, at most 200 listed) and its SKILL.md, cut to
 *  the request's bound. Only SKILL.md's text is sent, never another file's. */
export function skillReviewText(folder: string, files: readonly TarFile[]): string {
  const prefix = `${folder}/`;
  const rel = files.map((f) => (f.path.startsWith(prefix) ? f.path.slice(prefix.length) : f.path)).sort();
  const listed = rel.slice(0, MAX_LISTED).map((p) => `- ${p}`);
  if (rel.length > MAX_LISTED) listed.push(`- … and ${rel.length - MAX_LISTED} more`);
  const md = files.find((f) => f.path === `${prefix}${SKILL_MD}` || f.path === SKILL_MD);
  const head = `Skill folder: ${folder}\nFiles (${rel.length}):\n${listed.join("\n")}\n\n${SKILL_MD}:\n`;
  const body = md ? text.decode(md.data) : "(none)";
  const room = MaxAssistText - head.length;
  return room <= 0 ? head.slice(0, MaxAssistText) : head + (body.length > room ? `${body.slice(0, room - 1)}…` : body);
}

/** A library entry for the skill: what the publish sends beside its files. */
export interface SkillEntry {
  readonly name: string;
  readonly displayName: string;
  readonly description: string;
  readonly tags: readonly string[];
}

/** review-skill's answer: what to fix, and the entry it proposes (only the
 *  fields it gave in a form the library takes). */
export interface SkillReview {
  readonly findings: readonly string[];
  readonly proposed: Partial<SkillEntry>;
}

const MAX_FINDINGS = 6;
const MAX_DISPLAY_NAME = 120;
const MAX_DESCRIPTION = 1000;
const MAX_TAGS = 12;

type Fields = AssistJSONReply["fields"];

/** skillReviewFrom is a review-skill reply: at most six findings, a name
 *  made a valid component name, the display name and description within
 *  the library's bounds, the tags the tag rule admits. */
export function skillReviewFrom(fields: Fields): SkillReview {
  const findings = Array.isArray(fields.findings)
    ? fields.findings.filter((f): f is string => typeof f === "string").map((f) => f.trim()).filter(Boolean)
    : fieldText(fields, "findings") ? [fieldText(fields, "findings")] : [];
  const proposed: { -readonly [K in keyof SkillEntry]?: SkillEntry[K] } = {};
  const name = slugify(fieldText(fields, "name"));
  if (name) proposed.name = name;
  const displayName = fieldText(fields, "displayName").replace(/\s+/g, " ").slice(0, MAX_DISPLAY_NAME);
  if (displayName) proposed.displayName = displayName;
  const description = fieldText(fields, "description").replace(/\s+/g, " ").slice(0, MAX_DESCRIPTION);
  if (description) proposed.description = description;
  const tags = cleanTags(fieldList(fields, "tags"))
    .filter((t) => tagProblem([t]) === null)
    .slice(0, MAX_TAGS);
  if (tags.length > 0) proposed.tags = tags;
  return { findings: findings.slice(0, MAX_FINDINGS), proposed };
}
