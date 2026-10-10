import { initialValues, missingRequired, settingsUpdate } from "./pluginSettings.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function deepEqual(actual: unknown, expected: unknown): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
  }
}

const fields = [
  { key: "appId", label: "App ID", secret: false, required: true, set: true, value: "a1" },
  { key: "appKey", label: "App key", secret: true, required: true, set: true },
  { key: "note", label: "Note", secret: false, required: false, set: false },
];

test("local plugin settings: empty secrets keep the stored value", () => {
  deepEqual(missingRequired(fields, { appId: "a1", appKey: "" }), []);
  deepEqual(missingRequired(fields, { appId: " ", appKey: "" }), ["App ID"]);
  deepEqual(missingRequired([{ ...fields[1]!, set: false }], {}), ["App key"]);
  deepEqual(settingsUpdate(fields, { appId: " a2 ", appKey: "" }), { appId: "a2", appKey: "", note: "" });
});

test("local plugin settings: the form never pre-fills a secret", () => {
  deepEqual(initialValues(fields), { appId: "a1", appKey: "", note: "" });
});
