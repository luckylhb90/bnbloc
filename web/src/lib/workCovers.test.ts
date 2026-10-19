import { coverEncodings, createCoverTries, frameTime, workCoverPlan } from "./workCovers.ts";

// No node types in the web tsconfig: a local runner (see roomEvents.test.ts).
function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

// What a work without a cover gets, by type and viewer: never an AI cover
// for someone else's work, nor without image generation.
{
  const mine = { mine: true };
  eq(workCoverPlan({ ...mine, workType: "image" }, undefined, true), "image", "an image: itself");
  eq(workCoverPlan({ mine: false, workType: "image" }, undefined, false), "image", "anyone's image, no AI needed");
  eq(workCoverPlan({ mine: false, workType: "video" }, undefined, false), "frame", "a video: a frame");
  eq(workCoverPlan({ mine: false, workType: "document" }, "render", false), "viewer", "a PDF: its viewer's first page");
  eq(workCoverPlan({ mine: true, workType: "slides" }, "render", true), "viewer", "a deck its previewer draws: the viewer, no AI");
  eq(workCoverPlan({ ...mine, workType: "app" }, undefined, true), "ai", "the author's app: AI");
  eq(workCoverPlan({ ...mine, workType: "document" }, "text", true), "ai", "the author's Markdown (a text cover is never stored): AI");
  eq(workCoverPlan({ mine: false, workType: "site" }, undefined, true), null, "someone else's site: nothing");
  eq(workCoverPlan({ ...mine, workType: "audio" }, undefined, false), null, "no image generation: nothing");
}

// Sizes: at most 1600, 1200, then 800 wide, the aspect kept, never
// enlarged; each at two qualities.
{
  eq(
    coverEncodings(3200, 1800).map((e) => [e.width, e.height, e.quality]),
    [
      [1600, 900, 0.85],
      [1600, 900, 0.7],
      [1200, 675, 0.85],
      [1200, 675, 0.7],
      [800, 450, 0.85],
      [800, 450, 0.7],
    ],
    "a large image",
  );
  eq(
    coverEncodings(1000, 500).map((e) => e.width),
    [1000, 1000, 800, 800],
    "a narrower image keeps its width before 800",
  );
  eq(coverEncodings(0, 10), [], "no size");
}

// A video's frame: about a second in, the middle of a short clip.
{
  eq(frameTime(30), 1, "a long clip");
  eq(frameTime(1.2), 0.6, "a short clip's middle");
  eq(frameTime(Number.POSITIVE_INFINITY), 1, "an unknown length");
  eq(frameTime(Number.NaN), 0, "no length");
}

// A version is tried once per session.
{
  const tries = createCoverTries();
  eq([tries.take("w@1.0.0"), tries.take("w@1.0.0"), tries.take("w@1.1.0")], [true, false, true], "once per version");
}
