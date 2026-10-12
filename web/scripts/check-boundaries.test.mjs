import assert from "node:assert/strict";
import test from "node:test";
import { RULES, sourceFiles, violations } from "./check-boundaries.mjs";

test("web/src keeps every rule in its owner", () => {
  assert.deepEqual(violations(sourceFiles()), []);
});

test("every rule catches its sample and spares its owner", () => {
  for (const rule of RULES) {
    assert.equal(violations([rule.sample]).length, 1, `rule ${rule.id} misses its sample`);
    for (const file of rule.allowed) {
      assert.deepEqual(violations([{ file, text: rule.sample.text }]), [], `rule ${rule.id} flags its owner ${file}`);
    }
    assert.deepEqual(violations([{ file: "lib/x.test.ts", text: rule.sample.text }]), [], "tests are exempt");
  }
});
