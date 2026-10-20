import type { ComponentSetting } from "./componentSettings.ts";
import { fillRequestText, fillResult } from "./settingsFill.ts";
import { MaxAssistText } from "./wire.gen.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function eq(a: unknown, b: unknown, message: string): void {
  assert(JSON.stringify(a) === JSON.stringify(b), `${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const fields: ComponentSetting[] = [
  { key: "apiBase", label: "API base", type: "url", scope: "org", required: true, help: "Ends with /v1." },
  { key: "apiKey", label: "API key", type: "secret", scope: "org" },
  { key: "quality", label: "Quality", type: "choice", options: ["low", "high"], scope: "org" },
  { key: "verbose", label: "Verbose", type: "boolean", scope: "org" },
  { key: "dirs", label: "Folders", type: "lines", scope: "org" },
];

// The assist is asked with the form's fields, then the note.
{
  const asked = fillRequestText(fields, "  Use https://api.example.com/v1 with key sk-123.  ");
  eq(
    asked,
    [
      "Settings:",
      "- `apiBase` (url; required): API base — Ends with /v1.",
      "- `apiKey` (secret): API key",
      "- `quality` (choice; one of: low, high): Quality",
      "- `verbose` (boolean): Verbose",
      "- `dirs` (lines; one item per line): Folders",
      "",
      "Note:",
      "Use https://api.example.com/v1 with key sk-123.",
    ].join("\n"),
    "the request",
  );
  const long = fillRequestText(fields, "x".repeat(20000));
  eq([long.length, long.endsWith("…")], [MaxAssistText, true], "a long note is cut to the bound");
}

// The reply becomes proposals for the form's own fields only; a secret is
// never shown; what the form cannot take is said, by label.
{
  const r = fillResult(fields, {
    values: [
      { key: "apiKey", value: "sk-123" },
      { key: "apiBase", value: "https://api.example.com/v1" },
      { key: "apiBase", value: "https://other.example.com" },
      { key: "quality", value: "High" },
      { key: "verbose", value: "yes" },
      { key: "dirs", value: " ~/a \n\n~/b" },
      { key: "region", value: "eu" },
      { key: "note", value: "" },
    ],
    notes: "The note gives no region.",
  });
  eq(
    r.proposals,
    [
      { key: "apiBase", label: "API base", value: "https://api.example.com/v1", shown: "https://api.example.com/v1" },
      { key: "apiKey", label: "API key", value: "sk-123", shown: "a value from the note (hidden)" },
      { key: "quality", label: "Quality", value: "high", shown: "high" },
      { key: "dirs", label: "Folders", value: "~/a\n~/b", shown: "~/a\n~/b" },
    ],
    "proposals",
  );
  eq(r.skipped, ["region (not a setting here)", "Verbose (not true or false)"], "skipped");
  eq(r.notes, "The note gives no region.", "notes");
  assert(!JSON.stringify(r.proposals.map((p) => p.shown)).includes("sk-123"), "a secret is never shown");
  const many = Array.from({ length: 70 }, (_, i) => `~/d${i}`).join("\n");
  eq(fillResult(fields, { values: [{ key: "dirs", value: many }] }).skipped, ["Folders (at most 64 lines (this has 70))"], "a list over the bound");
  eq(fillResult(fields, {}), { proposals: [], skipped: [], notes: "" }, "an empty reply");
}
