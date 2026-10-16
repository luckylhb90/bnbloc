import { entryScriptFromHtml, isChunkLoadError } from "./webBuild.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
const assert = {
  equal(actual: unknown, expected: unknown): void {
    if (actual !== expected) throw new Error(`expected ${String(expected)}, got ${String(actual)}`);
  },
};

test("entryScriptFromHtml finds the hashed entry of a built index.html", () => {
  const html = `<!doctype html><html><head>
    <script type="module" crossorigin src="./assets/index-CaezWxTa.js"></script>
    <link rel="stylesheet" crossorigin href="./assets/index-BZvjuMah.css">
  </head><body><div id="root"></div></body></html>`;
  assert.equal(entryScriptFromHtml(html), "index-CaezWxTa.js");
});

test("entryScriptFromHtml ignores non-entry scripts and dev pages", () => {
  assert.equal(entryScriptFromHtml(`<script type="module" src="/src/main.tsx"></script>`), null);
  assert.equal(entryScriptFromHtml(`<script src="./assets/vendor-x1.js"></script>`), null);
  assert.equal(entryScriptFromHtml("404 page not found"), null);
});

test("isChunkLoadError knows a chunk a deploy removed, in each engine", () => {
  assert.equal(isChunkLoadError(new TypeError("Failed to fetch dynamically imported module: https://x/assets/ImageViewer-BLoyj3Vy.js")), true);
  assert.equal(isChunkLoadError(new TypeError("error loading dynamically imported module: https://x/a.js")), true);
  assert.equal(isChunkLoadError(new TypeError("Importing a module script failed.")), true);
  assert.equal(isChunkLoadError(new Error("Unable to preload CSS for /assets/a.css")), true);
  assert.equal(isChunkLoadError("Failed to fetch dynamically imported module"), true);
  assert.equal(isChunkLoadError(new Error("Invalid JSON")), false);
  assert.equal(isChunkLoadError(undefined), false);
});
