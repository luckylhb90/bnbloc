import { POSTER_LIMIT_MS, posterVisible, type PosterState } from "./sharePoster.ts";

// The web tsconfig has no node types, so the tests use a local runner (same
// convention as the other *.test.ts files); a failure throws and fails `node --test`.
function test(name: string, fn: () => void): void {
  try {
    fn();
  } catch (e) {
    throw new Error(`${name}: ${e instanceof Error ? e.message : String(e)}`);
  }
}
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

const loading: PosterState = { cover: true, viewerIsPreviewer: true, rendered: false, errored: false, elapsedMs: 0 };

test("the cover shows while a previewer loads a file that has one", () => {
  eq(posterVisible(loading), true, "at the start");
  eq(posterVisible({ ...loading, elapsedMs: POSTER_LIMIT_MS - 1 }), true, "just before the limit");
});

test("no cover, or a built-in viewer, means no picture", () => {
  eq(posterVisible({ ...loading, cover: false }), false, "no cover stored");
  eq(posterVisible({ ...loading, viewerIsPreviewer: false }), false, "a built-in viewer draws at once");
  eq(posterVisible({ ...loading, cover: false, viewerIsPreviewer: false }), false, "neither");
});

test("the picture goes when the previewer has drawn, or failed", () => {
  eq(posterVisible({ ...loading, rendered: true }), false, "drawn");
  eq(posterVisible({ ...loading, errored: true }), false, "errored: its notice must show");
  eq(posterVisible({ ...loading, rendered: true, errored: true }), false, "both");
});

test("the picture goes at the limit even if the previewer said nothing", () => {
  eq(posterVisible({ ...loading, elapsedMs: POSTER_LIMIT_MS }), false, "at the limit");
  eq(posterVisible({ ...loading, elapsedMs: POSTER_LIMIT_MS * 3 }), false, "long past it");
});
