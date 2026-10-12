// Subjects (user ids, owners, authors) are case-sensitive and compare
// exactly, as on the server (docs/prd/2026-10-12-code-health-remediation.md,
// D2). The web client has one same-subject check, sameUser in lib/format.ts;
// these tests keep a second copy, or a case fold of a subject, from coming
// back. A fold of something that only looks like a subject carries an
// "identity-fold: <reason>" comment on its line.
import assert from "node:assert/strict";
import test from "node:test";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { dedupeMembers, sameUser } from "../src/lib/format.ts";

const SRC = fileURLToPath(new URL("../src/", import.meta.url));

function sources(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      sources(path, out);
    } else if (/\.(ts|tsx)$/.test(name) && !/\.test\.tsx?$/.test(name)) {
      out.push(path);
    }
  }
  return out;
}

// A subject-naming expression (optionally trimmed) folded to one case.
const FOLD =
  /\b(?:subject|owner|ownerId|ownerUserId|userId|createdBy|author|requester|requesterUserId|hostUserId|member|memberId|removedUser|presenceUser|actor)\??(?:\.trim\(\))?\.to(?:Lower|Upper)Case\(\)/;
const COPY = /\bfunction\s+sameUser\b|\b(?:const|let)\s+sameUser\s*=/;

test("sameUser compares subjects exactly", () => {
  assert.equal(sameUser("maya", "maya"), true);
  assert.equal(sameUser("maya", "Maya"), false);
  assert.equal(sameUser("josé", "jose"), false);
  assert.equal(sameUser("", ""), false);
  assert.equal(sameUser(null, "maya"), false);
  assert.deepEqual(dedupeMembers(["maya", "Maya", "maya", "jack"]), ["maya", "Maya", "jack"]);
});

test("only lib/format.ts defines sameUser", () => {
  const copies = sources(SRC)
    .filter((path) => relative(SRC, path) !== join("lib", "format.ts"))
    .filter((path) => COPY.test(readFileSync(path, "utf8")))
    .map((path) => relative(SRC, path));
  assert.deepEqual(copies, [], `import sameUser from lib/format.ts instead of redefining it: ${copies.join(", ")}`);
});

test("no case fold on a subject", () => {
  const folds = [];
  for (const path of sources(SRC)) {
    readFileSync(path, "utf8")
      .split("\n")
      .forEach((line, i) => {
        if (FOLD.test(line) && !line.includes("identity-fold:")) folds.push(`${relative(SRC, path)}:${i + 1}: ${line.trim()}`);
      });
  }
  assert.deepEqual(folds, [], `subjects are case-sensitive; compare with sameUser or ===:\n${folds.join("\n")}`);
});
