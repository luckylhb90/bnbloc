import { centered, cropMimeType, fitScale, regionRect, regionSelection, spanRegion, toImage, visibleGrid, zoomAt } from "./imageView.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) {
    throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
  }
}

eq(fitScale({ w: 200, h: 100 }, { w: 100, h: 100 }), 0.5, "fit width-bound");
eq(fitScale({ w: 10, h: 10 }, { w: 100, h: 50 }), 5, "small images grow to fit");
eq(centered({ w: 100, h: 50 }, { w: 300, h: 150 }, 1), { scale: 1, x: 100, y: 50 }, "centered");
eq(zoomAt({ scale: 1, x: 0, y: 0 }, 2, 50, 50), { scale: 2, x: -50, y: -50 }, "point under cursor stays fixed");
eq(zoomAt({ scale: 1, x: 0, y: 0 }, 1000, 0, 0).scale, 64, "clamped");
eq(visibleGrid({ scale: 8, x: -20, y: 10 }, { w: 100, h: 100 }, { w: 200, h: 200 }), { left: 0, top: 10, width: 200, height: 190, offsetX: 4, offsetY: 0 }, "grid clipped to view");
eq(visibleGrid({ scale: 4, x: 500, y: 0 }, { w: 10, h: 10 }, { w: 200, h: 200 }), null, "off-screen");

// An area selected on the image (docs/prd/2026-10-14-select-edit-review.md rule 7).
{
  const view = { scale: 2, x: 10, y: 20 };
  const image = { w: 100, h: 50 };
  const a = toImage(view, image, 30, 40);
  const b = toImage(view, image, 9999, -5);
  const r = spanRegion(a, b);
  if (JSON.stringify([a, b, r]) !== JSON.stringify([{ x: 10, y: 10 }, { x: 100, y: 0 }, { x: 10, y: 0, w: 90, h: 10 }])) throw new Error(`region ${JSON.stringify([a, b, r])}`);
  if (JSON.stringify(regionRect(view, r)) !== JSON.stringify({ x: 30, y: 20, w: 180, h: 20 })) throw new Error("rect");
  const s = regionSelection(r, image);
  if (s.label !== "Area 90×10 at (10, 0)" || !s.detail.includes("(100, 10)") || !s.detail.includes("100×50 image")) throw new Error(JSON.stringify(s));
}

{
  const r = { x: 10, y: 0, w: 90, h: 10 };
  const plain = regionSelection(r, { w: 100, h: 50 });
  const named = regionSelection(r, { w: 100, h: 50 }, ["Start", "P2P check passed?"]);
  if (named.label !== plain.label) throw new Error("the chip does not change");
  if (!named.detail.startsWith(plain.detail) || !named.detail.endsWith(" Labels in it: “Start”, “P2P check passed?”.")) throw new Error(named.detail);
  if (regionSelection(r, { w: 100, h: 50 }, []).detail !== plain.detail) throw new Error("no labels, no sentence");
}

// A crop keeps the image's type where the browser encodes it, else PNG.
eq(["image/jpeg", "image/webp", "image/png", "image/gif", "image/svg+xml"].map(cropMimeType), ["image/jpeg", "image/webp", "image/png", "image/png", "image/png"], "crop type");
