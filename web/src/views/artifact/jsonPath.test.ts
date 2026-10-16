import { childPath, valueText } from "./jsonPath.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq(childPath("$", "items", false), "$.items", "an identifier key");
eq(childPath("$.items", "2", true), "$.items[2]", "an array index");
eq(childPath("$.items[2]", "name", false), "$.items[2].name", "nested");
eq(childPath("$", "odd key", false), '$["odd key"]', "a key that is not an identifier");
eq(childPath("$", "1st", false), '$["1st"]', "a key that starts with a digit");
eq(childPath("$", 'q"uote', false), '$["q\\"uote"]', "a quote is escaped");
eq(valueText("x"), '"x"', "a string is quoted");
eq(valueText(3), "3", "a number");
eq(valueText(null), "null", "null");
eq(valueText("a".repeat(600)).length, 500, "cut at the limit");
eq(valueText("a".repeat(600)).endsWith('…'), true, "with an ellipsis");
