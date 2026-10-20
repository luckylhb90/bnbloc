import { fieldItems, fieldText } from "./assistDrafts.ts";
import { isLines, isSecret, linesProblem, linesValue, parseLines, type ComponentSetting } from "./componentSettings.ts";
import { AssistTaskFillSettings, MaxAssistText, type AssistJSONReply } from "./wire.gen.ts";

// "Fill from a note" on the one settings form (docs/prd/2026-10-19-
// component-settings.md §2.5), its pure side: the text the fill-settings
// assist is asked with (the fields the form edits: key, type, label, help,
// options; then the pasted note) and its reply as proposals the person
// ticks before they reach the form. Nothing is saved here: an applied
// proposal is a value in the form like a typed one, held to the same
// helpers on save (componentSettings.ts) and then to the platform's rule.
// A secret's proposed value is never shown, only that there is one.
// Unit-tested.

/** The assist that maps a note onto a declaration (resolved by name; its output contract is wire.AssistContracts). */
export const FILL_SETTINGS = AssistTaskFillSettings;

const NOTE = "\n\nNote:\n";

function describe(f: ComponentSetting): string {
  const kind = [f.type, f.options?.length ? `one of: ${f.options.join(", ")}` : "", isLines(f) ? "one item per line" : "", f.required ? "required" : ""].filter(Boolean).join("; ");
  return `- \`${f.key}\` (${kind}): ${f.label}${f.help ? ` — ${f.help}` : ""}`;
}

/** fillRequestText is fill-settings' text: the settings, then the note
 *  (cut to fit the request's bound). */
export function fillRequestText(fields: readonly ComponentSetting[], note: string): string {
  const head = `Settings:\n${fields.map(describe).join("\n")}`.slice(0, MaxAssistText / 2);
  const room = MaxAssistText - head.length - NOTE.length;
  const body = note.trim();
  return head + NOTE + (body.length > room ? `${body.slice(0, room - 1)}…` : body);
}

/** One value the assist read from the note, for the person to tick. */
export interface FillProposal {
  readonly key: string;
  readonly label: string;
  readonly value: string;
  // What the person sees: a secret only as "a value from the note".
  readonly shown: string;
}

/** What the reply proposes, what it gave that the form cannot take (and
 *  why, by label), and what the note left unclear. */
export interface FillResult {
  readonly proposals: readonly FillProposal[];
  readonly skipped: readonly string[];
  readonly notes: string;
}

/** proposalValue is a value as the form takes it, or why not: a list as its
 *  lines within the bounds, a switch "true" or "false", a choice one of its
 *  options (matched without case). */
function proposalValue(f: ComponentSetting, raw: string): { value: string } | { why: string } {
  if (isLines(f)) {
    const why = linesProblem(parseLines(raw));
    return why ? { why } : { value: linesValue([raw]) };
  }
  if (f.type === "boolean") {
    const v = raw.toLowerCase();
    return v === "true" || v === "false" ? { value: v } : { why: "not true or false" };
  }
  if (f.type === "choice") {
    const option = (f.options ?? []).find((o) => o.toLowerCase() === raw.toLowerCase());
    return option ? { value: option } : { why: "not one of its options" };
  }
  return { value: raw };
}

/** fillResult is a fill-settings reply onto the fields a form edits: one
 *  proposal per field the note gave a value for (the first, in the form's
 *  order), every other key said as skipped. */
export function fillResult(fields: readonly ComponentSetting[], reply: AssistJSONReply["fields"]): FillResult {
  const given = new Map<string, string>();
  const skipped: string[] = [];
  for (const item of fieldItems(reply, "values")) {
    const key = item.key ?? "";
    const value = item.value ?? "";
    if (!key || !value || given.has(key)) continue;
    if (!fields.some((f) => f.key === key)) skipped.push(`${key} (not a setting here)`);
    else given.set(key, value);
  }
  const proposals: FillProposal[] = [];
  for (const f of fields) {
    const raw = given.get(f.key);
    if (raw === undefined) continue;
    const v = proposalValue(f, raw);
    if ("why" in v) {
      skipped.push(`${f.label} (${v.why})`);
      continue;
    }
    proposals.push({ key: f.key, label: f.label, value: v.value, shown: isSecret(f) ? "a value from the note (hidden)" : v.value });
  }
  return { proposals, skipped, notes: fieldText(reply, "notes") };
}
