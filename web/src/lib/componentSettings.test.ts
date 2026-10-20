import settingValues from "../../../core/pkgmeta/testdata/setting_values.json" with { type: "json" };
import {
  initialValues,
  linesProblem,
  linesValue,
  missingRequired,
  needsSetupText,
  ofScope,
  parseLines,
  scopeText,
  settingInput,
  settingsProblems,
  settingsSummary,
  settingsUpdate,
  shownValue,
  type ComponentSettingField,
} from "./componentSettings.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function equal(actual: unknown, expected: unknown): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
  }
}

const fields: ComponentSettingField[] = [
  { key: "apiBase", label: "API base", type: "url", scope: "org", required: true, set: true, value: "https://a.example.com" },
  { key: "apiKey", label: "API key", type: "secret", scope: "org", required: true, set: true },
  { key: "note", label: "Note", type: "text", scope: "org", set: false },
  { key: "quality", label: "Quality", type: "choice", options: ["low", "high"], default: "low", scope: "org", required: true, set: false },
  { key: "token", label: "Token", type: "secret", scope: "org", set: true },
];

test("a form starts from the stored values; secrets and defaults start empty", () => {
  equal(initialValues(fields), { apiBase: "https://a.example.com", apiKey: "", note: "", quality: "", token: "" });
  equal(shownValue(fields[3]!, ""), "low");
  equal(shownValue(fields[3]!, "high"), "high");
});

test("the update keeps secrets left empty and clears what was emptied", () => {
  const values = { apiBase: " https://b.example.com ", apiKey: "", note: "", quality: "high", token: "" };
  equal(settingsUpdate(fields, values), { values: { apiBase: "https://b.example.com", note: "", quality: "high" } });
  // A new secret is sent; a stored one cleared on purpose goes in clear.
  equal(settingsUpdate(fields, { ...values, apiKey: "s3cr3t" }, ["token"]), {
    values: { apiBase: "https://b.example.com", apiKey: "s3cr3t", note: "", quality: "high" },
    clear: ["token"],
  });
});

test("required fields: a stored secret and a default count as filled", () => {
  equal(missingRequired(fields, initialValues(fields)), []);
  equal(missingRequired(fields, { ...initialValues(fields), apiBase: " " }), ["API base"]);
  const fresh = fields.map((f) => ({ ...f, set: false, value: undefined }) as ComponentSettingField);
  equal(missingRequired(fresh, initialValues(fresh)), ["API base", "API key"]);
});

test("each type renders its control; each scope says who sets it", () => {
  equal(settingInput("secret"), { control: "input", type: "password" });
  equal(settingInput("path"), { control: "input", type: "text" });
  equal(settingInput("boolean"), { control: "switch" });
  equal(settingInput("choice"), { control: "select" });
  equal(scopeText("org"), "Set by your organization");
  equal(scopeText("platform"), "Set by the platform");
  equal(scopeText("member"), "Yours, on this computer");
});

test("needs setup names the missing settings by label", () => {
  equal(needsSetupText(["apiKey", "gone"], fields), "Needs setup: API key, gone");
  equal(needsSetupText([], fields), "");
  equal(ofScope(fields, "member"), []);
  equal(settingsSummary(fields.slice(0, 2)), "apiBase (url, org) *, apiKey (secret, org) *");
});

const folders: ComponentSettingField = { key: "dirs", label: "Folders", type: "lines", scope: "member", set: true, value: "~/a\n~/b" };

test("a list is edited one per line and sent as its lines", () => {
  equal(settingInput("lines"), { control: "lines" });
  equal(parseLines(" ~/a \r\n\n~/b\r~/c\n  "), ["~/a", "~/b", "~/c"]);
  equal(linesValue(["~/a", " ", " ~/b"]), "~/a\n~/b");
  equal(initialValues([folders]), { dirs: "~/a\n~/b" });
  equal(settingsUpdate([folders], { dirs: "~/a\n\n  ~/c  \n" }), { values: { dirs: "~/a\n~/c" } });
  equal(settingsUpdate([folders], { dirs: " \n " }), { values: { dirs: "" } });
  equal(missingRequired([{ ...folders, required: true, set: false }], { dirs: "\n  \n" }), ["Folders"]);
});

test("a list is held to the platform's bounds before it is sent", () => {
  equal(linesProblem([]), "");
  equal(linesProblem(Array.from({ length: 65 }, (_, i) => `l${i}`)), "at most 64 lines (this has 65)");
  equal(linesProblem(["ok", "é".repeat(257)]), "line 2 is longer than 512 bytes");
  equal(settingsProblems([folders, fields[2]!], { dirs: "x".repeat(513), note: "x".repeat(9999) }), ["Folders: line 1 is longer than 512 bytes"]);
});

// The platform's own cases (core/pkgmeta's CheckSettingValue, the desktop's
// copy): a value the form would send (its lines, trimmed, none empty) within
// the bounds is exactly a value the platform keeps.
test("the form's lines rule agrees with the platform's", () => {
  for (const c of settingValues.values.filter((v) => v.setting.type === "lines")) {
    const lines = c.value.split("\n");
    const asSent = linesValue(lines) === c.value && !c.value.includes("\u0000");
    equal([c.value.slice(0, 20), asSent && linesProblem(lines) === ""], [c.value.slice(0, 20), c.ok]);
  }
});
