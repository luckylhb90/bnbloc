// The AI check of a room component's text before it is saved
// (docs/prd/2026-10-20-rooms-as-components.md §6, "Checked before it
// leaves the room"): the describe-room assist reads every text the package
// would carry, each under its key (roomPackageTexts), and answers its
// findings (secrets, internal links, people's names, private data), which
// the save dialog shows with "Fix" (edit that field, or leave the part
// out) and "Ignore". Saving stays the member's decision. The helpers are
// pure: the input, the reply read, which findings still hold, a fix.

import { describeRoomInput, isOn, partKey, type RoomSaveForm } from "./roomComponents.ts";

/** The kinds a finding may be, in the order describe-room's item
 *  description names them (a test holds them together). */
export const FINDING_KINDS = ["secret", "internal-link", "personal-name", "private-data"] as const;
export type FindingKind = (typeof FINDING_KINDS)[number];

/** FINDING_LABELS are the kinds in words. */
export const FINDING_LABELS: Readonly<Record<FindingKind, string>> = {
  secret: "A secret",
  "internal-link": "An internal link",
  "personal-name": "A person's name",
  "private-data": "Private data",
};

/** At most this many findings are shown. */
const MAX_FINDINGS = 10;

/** A finding: the package text it is in (its key), the words in question,
 *  their kind (null when the reply named none of FINDING_KINDS) and why. */
export interface RoomFinding {
  readonly field: string;
  readonly quote: string;
  readonly kind: FindingKind | null;
  readonly why: string;
}

/** roomPackageTexts is every text the saved package would carry, by key:
 *  displayName, description, welcome, instructions (when there are any),
 *  quickPrompt:<n>, schedule:<n> (a kept one, n its index in the form),
 *  channel:<name> (a kept one) and source:<name> (its purpose). */
export function roomPackageTexts(f: RoomSaveForm): Record<string, string> {
  const out: Record<string, string> = { displayName: f.displayName, description: f.description, welcome: f.welcome };
  if (f.instructions.trim()) out.instructions = f.instructions;
  f.quickPrompts.forEach((q, i) => {
    out[`quickPrompt:${i}`] = [q.title, q.prompt].filter(Boolean).join(": ");
  });
  f.schedules.forEach((s, i) => {
    if (isOn(f, partKey("schedule", i))) out[`schedule:${i}`] = s.text;
  });
  for (const c of f.channels) if (isOn(f, partKey("channel", c))) out[`channel:${c}`] = c;
  for (const s of f.sources) out[`source:${s.name}`] = s.purpose;
  return out;
}

/** roomReviewInput is describe-room's <text>: the setup and the member's
 *  values (describeRoomInput) and, under package, the texts to check. */
export function roomReviewInput(f: RoomSaveForm): string {
  return JSON.stringify({ ...(JSON.parse(describeRoomInput(f)) as object), package: roomPackageTexts(f) });
}

/** reviewKey identifies the texts a check read: a save after a change is
 *  checked again. */
export function reviewKey(f: RoomSaveForm): string {
  return JSON.stringify(roomPackageTexts(f));
}

const str = (v: unknown): string => (typeof v === "string" ? v.trim() : "");

/** roomFindings reads describe-room's findings for the form: only those in
 *  a text the package carries, at most MAX_FINDINGS, once each. */
export function roomFindings(fields: Readonly<Record<string, unknown>>, f: RoomSaveForm): RoomFinding[] {
  const texts = roomPackageTexts(f);
  const raw = Array.isArray(fields.findings) ? fields.findings : [];
  const out: RoomFinding[] = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const it = item as Record<string, unknown>;
    const field = str(it.field);
    if (!(field in texts)) continue;
    const quote = str(it.quote).slice(0, 120);
    const why = str(it.why);
    const head = why.split(":")[0].trim().toLowerCase();
    const kind = (FINDING_KINDS as readonly string[]).includes(head) ? (head as FindingKind) : null;
    const finding = { field, quote, kind, why: kind ? why.slice(why.indexOf(":") + 1).trim() : why };
    if (!out.some((o) => o.field === finding.field && o.quote === finding.quote)) out.push(finding);
    if (out.length === MAX_FINDINGS) break;
  }
  return out;
}

/** findingId names a finding (Ignore keeps it). */
export function findingId(x: RoomFinding): string {
  return `${x.field}\n${x.quote}`;
}

/** openFindings are the findings still to decide: not ignored, and still
 *  in the package (its text kept, the words still in it). */
export function openFindings(findings: readonly RoomFinding[], f: RoomSaveForm, ignored: readonly string[]): RoomFinding[] {
  const texts = roomPackageTexts(f);
  return findings.filter((x) => !ignored.includes(findingId(x)) && x.field in texts && (!x.quote || texts[x.field].includes(x.quote)));
}

/** A field the save dialog edits in place. */
export type EditableField = "displayName" | "description" | "welcome" | "instructions" | `source:${string}`;

/** A finding's fix: the field to edit (the dialog focuses it), or the form
 *  without the part (a schedule or channel left out, a quick prompt
 *  removed: the dialog does not edit their text). */
export type FindingFix = { readonly edit: EditableField } | { readonly form: RoomSaveForm; readonly label: string };

/** fixFinding is what "Fix" does for x. */
export function fixFinding(f: RoomSaveForm, x: RoomFinding): FindingFix {
  const [kind, id] = [x.field.split(":")[0], x.field.slice(x.field.indexOf(":") + 1)];
  switch (kind) {
    case "schedule":
      return { form: { ...f, off: [...f.off, partKey("schedule", Number(id))] }, label: "Left the scheduled task out" };
    case "channel":
      return {
        // A kept task runs in its channel: it goes with it.
        form: {
          ...f,
          off: [...f.off, partKey("channel", id), ...f.schedules.flatMap((s, i) => (s.channel === id ? [partKey("schedule", i)] : []))],
        },
        label: `Left #${id} out`,
      };
    case "quickPrompt":
      return { form: { ...f, quickPrompts: f.quickPrompts.filter((_, i) => i !== Number(id)) }, label: "Removed the quick prompt" };
    case "source":
      return { edit: x.field as EditableField };
  }
  return { edit: x.field as EditableField };
}

/** findingWhere is where a finding is, in words. */
export function findingWhere(f: RoomSaveForm, x: RoomFinding): string {
  const [kind, id] = [x.field.split(":")[0], x.field.slice(x.field.indexOf(":") + 1)];
  switch (kind) {
    case "displayName":
      return "Display name";
    case "description":
      return "Description";
    case "welcome":
      return "Welcome";
    case "instructions":
      return "Instructions";
    case "schedule":
      return `Scheduled task ${Number(id) + 1}`;
    case "channel":
      return `Channel #${id}`;
    case "quickPrompt":
      return `Quick prompt "${f.quickPrompts[Number(id)]?.title ?? Number(id) + 1}"`;
    case "source":
      return "A source's purpose";
  }
  return x.field;
}
