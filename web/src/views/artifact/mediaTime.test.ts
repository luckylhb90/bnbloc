import { clock, rangeSelection } from "./mediaTime.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq([clock(0), clock(75.4), clock(3725), clock(-3)], ["0:00", "1:15", "1:02:05", "0:00"], "clock");
eq(rangeSelection(18.42, 12, 90), { label: "0:12–0:18", detail: "From 12.0 s to 18.4 s (0:12 to 0:18) of 1:30." }, "a range, either order");
