// Web boundary rule 7 (docs/prd/2026-10-12-code-health-remediation.md): the
// acting tenant has one owner, lib/session.ts (useActingTenant). No other
// source spells the header or its storage key, builds a ?tenantId= override
// (the gateway reads none), or reaches into the acting-tenant state; the
// auth layer only binds it to the signed-in subject and clears it at
// sign-out.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const src = fileURLToPath(new URL("../src/", import.meta.url));

function sources(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) out.push(...sources(path));
    else if (/\.(ts|tsx)$/.test(name) && !/\.test\.tsx?$/.test(name)) out.push(path);
  }
  return out;
}

const rules = [
  { what: "the acting-tenant header", pattern: /X-Bnbloc-Acting-Tenant/, owners: ["lib/session.ts"] },
  { what: "the acting-tenant storage key", pattern: /bnbloc\.actingTenant/, owners: ["lib/session.ts"] },
  { what: "a ?tenantId= override", pattern: /[?&]tenantId=/, owners: [] },
  { what: "the acting-tenant state", pattern: /\b(getActingTenant|setActingTenant)\b/, owners: ["lib/session.ts"] },
  { what: "the acting-tenant binding", pattern: /\b(bindActingTenant|clearActingTenant)\b/, owners: ["lib/session.ts", "lib/auth.tsx"] },
];

test("rule 7: the acting tenant is owned by lib/session.ts", () => {
  const problems = [];
  for (const file of sources(src)) {
    const rel = relative(src, file).split("\\").join("/");
    const text = readFileSync(file, "utf8");
    for (const rule of rules) {
      if (rule.pattern.test(text) && !rule.owners.includes(rel)) problems.push(`${rel}: ${rule.what}`);
    }
  }
  assert.deepEqual(problems, []);
});
