import { diffPixels } from "./imageDiff.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

const px = (...rgba: number[][]) => new Uint8ClampedArray(rgba.flat());
const { out, changed } = diffPixels(px([10, 10, 10, 255], [100, 100, 100, 255], [5, 5, 5, 255]), px([12, 10, 10, 255], [200, 100, 100, 255], [5, 5, 5, 255]));
eq(changed, 1, "noise below the threshold is not a change");
eq([...out.slice(4, 8)], [239, 68, 68, 255], "a changed pixel is marked");
eq([...out.slice(0, 4)], [12, 10, 10, 64], "an unchanged pixel is the newer image, faint");
