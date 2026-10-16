// node --test examples/previewers/mermaid/build.test.mjs
// (after `pnpm --dir examples/previewers/mermaid install --frozen-lockfile`)
// Builds the Mermaid previewer into a temp dir and checks the bundle the way
// the gateway and desktop will read it (../lib/bundle-check.mjs), then the
// source helpers (src/source.ts).

import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { build } from "./build.mjs";
import { assertBundle } from "../lib/bundle-check.mjs";
import { linesNaming, nodeIdOf, parseErrorMessage, sourceUnits } from "./src/source.ts";

const HERE = dirname(fileURLToPath(import.meta.url));

test("the build produces a deterministic, parseable bundle with its notices", async () => {
  const outA = await mkdtemp(join(tmpdir(), "mermaid-a-"));
  const outB = await mkdtemp(join(tmpdir(), "mermaid-b-"));
  try {
    const { files, pkg } = await assertBundle(await build({ outDir: outA, quiet: true }), await build({ outDir: outB, quiet: true }), {
      files: ["NOTICES.txt", "index.html", "main.js", "style.css"],
      toolbar: ["zoom", "fit"],
    });
    assert.deepEqual(pkg.previewer.match.extensions, [".mmd", ".mermaid"]);
    assert.deepEqual(pkg.previewer.capabilities, ["thumbnail", "toolbar", "selection", "units"]);
    const notices = files.get("NOTICES.txt").toString("utf8");
    for (const name of ["mermaid", "d3", "dompurify"]) assert.match(notices, new RegExp(`^${name}@`, "m"), `${name} attributed`);
  } finally {
    await rm(outA, { recursive: true, force: true });
    await rm(outB, { recursive: true, force: true });
  }
});

const FLOW = await readFile(join(HERE, "samples", "flow.mmd"), "utf8");

test("a picked label names the source lines that mention it", () => {
  assert.deepEqual(linesNaming(FLOW, "P2P check passed?"), ["line 3: A([Start]) --> B{P2P check passed?}"]);
  assert.deepEqual(linesNaming(FLOW, "yes"), ["line 4: B -->|yes| C[Local Team hand-off]"]);
  // A label mermaid broke across lines is found by its words in order.
  assert.deepEqual(linesNaming(FLOW, "Local Team\n hand-off"), ["line 4: B -->|yes| C[Local Team hand-off]"]);
  // Comments never name a part; an unknown label names nothing.
  assert.deepEqual(linesNaming("%% P2P check\nA-->B", "P2P check"), []);
  assert.deepEqual(linesNaming(FLOW, "nowhere"), []);
  assert.deepEqual(linesNaming(FLOW, "  "), []);
  assert.equal(linesNaming("A-->B\nA-->B\nA-->B\nA-->B\nA-->B", "A-->B").length, 3, "at most three lines");
});

test("a node's id comes from the element mermaid drew it as", () => {
  assert.equal(nodeIdOf("bnbloc-mermaid-1-flowchart-B-1", "bnbloc-mermaid-1"), "B");
  assert.equal(nodeIdOf("bnbloc-mermaid-7-state-Idle-12", "bnbloc-mermaid-7"), "Idle");
  assert.equal(nodeIdOf("bnbloc-mermaid-1-flowchart-my-node-4", "bnbloc-mermaid-1"), "my-node", "ids with dashes");
  assert.equal(nodeIdOf("bnbloc-mermaid-1", "bnbloc-mermaid-1"), "", "the drawing itself");
  assert.equal(nodeIdOf("", "bnbloc-mermaid-1"), "", "an arrow's label has no id");
  assert.equal(nodeIdOf("other-flowchart-B-1", "bnbloc-mermaid-1"), "", "another drawing's part");
});

test("Compare reads the statements, not the comments or blank lines", () => {
  const units = sourceUnits(FLOW);
  assert.equal(units[0].text, "flowchart TD");
  assert.ok(units.every((u) => !u.text.startsWith("%%") && u.text.length > 0));
  assert.deepEqual(units[1], { label: "Line 3", text: "A([Start]) --> B{P2P check passed?}" });
  assert.equal(units.length, 7, "seven statements; the comment line is dropped");
  assert.deepEqual(sourceUnits(""), []);
});

test("a parse error is a short message", () => {
  assert.equal(parseErrorMessage(new Error("Parse error on line 3:\n...")), "Parse error on line 3:\n...");
  assert.equal(parseErrorMessage("plain"), "plain");
  assert.ok(parseErrorMessage(new Error("x".repeat(1000))).length <= 400);
});
