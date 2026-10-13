import { appCover, appInitial } from "./appCover.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// Deterministic: the same app always draws the same cover; the name only
// picks the initial.
const a = appCover("art_01J9ZPOLL", "Friday poll");
assert(JSON.stringify(a) === JSON.stringify(appCover("art_01J9ZPOLL", "Friday poll")), "the same app draws the same cover");
assert(appCover("art_01J9ZPOLL", "Renamed").background === a.background, "a rename keeps the gradient");
assert(a.initial === "F", `initial ${a.initial}`);
assert(/^linear-gradient\(\d+deg, hsl\(\d+ 70% 52%\), hsl\(\d+ 75% 40%\)\)$/.test(a.background), `background ${a.background}`);

// Different apps draw different covers (no collisions across a sample),
// and the two stops never share a hue.
const backgrounds = new Set<string>();
for (let i = 0; i < 200; i++) {
  const c = appCover(`art_${i.toString(36)}_app`, "x");
  backgrounds.add(c.background);
  const hues = [...c.background.matchAll(/hsl\((\d+) /g)].map((m) => Number(m[1]));
  assert(hues.length === 2 && hues[0] !== hues[1], `one hue twice: ${c.background}`);
}
assert(backgrounds.size >= 190, `only ${backgrounds.size} distinct covers of 200 apps`);

// The initial is the name's first letter or digit.
assert(appInitial("  friday poll") === "F", "leading space");
assert(appInitial("123 go") === "1", "digit");
assert(appInitial("été") === "É", "accented");
assert(appInitial("🎉 party") === "P", "emoji skipped");
assert(appInitial("") === "" && appInitial("—") === "", "no letter");
