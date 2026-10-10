import { centered, fitScale, visibleGrid, zoomAt } from "./imageView.ts";

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
