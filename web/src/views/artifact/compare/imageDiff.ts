// Which pixels changed between two images of the same size (Compare's
// Difference view, docs/prd/2026-10-14-select-edit-review.md rule 7). The
// output paints the newer image faintly and every changed pixel in a strong
// colour, so an edit's area stands out. Pure (typed arrays in, typed array
// out), unit-tested under node.

/** A pixel changed when its channels differ by more than this in all
 *  (ignores compression noise). */
export const DIFF_THRESHOLD = 32;

export function diffPixels(
  before: Uint8ClampedArray,
  after: Uint8ClampedArray,
  mark: readonly [number, number, number] = [239, 68, 68],
): { readonly out: Uint8ClampedArray; readonly changed: number } {
  const out = new Uint8ClampedArray(after.length);
  let changed = 0;
  for (let i = 0; i < after.length; i += 4) {
    const d = Math.abs(after[i] - before[i]) + Math.abs(after[i + 1] - before[i + 1]) + Math.abs(after[i + 2] - before[i + 2]) + Math.abs(after[i + 3] - before[i + 3]);
    if (d > DIFF_THRESHOLD) {
      changed++;
      out[i] = mark[0];
      out[i + 1] = mark[1];
      out[i + 2] = mark[2];
      out[i + 3] = 255;
    } else {
      // Unchanged: the newer image, faint.
      out[i] = after[i];
      out[i + 1] = after[i + 1];
      out[i + 2] = after[i + 2];
      out[i + 3] = Math.round(after[i + 3] * 0.25);
    }
  }
  return { out, changed };
}
