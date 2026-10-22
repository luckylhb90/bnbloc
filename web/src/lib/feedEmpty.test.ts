import { feedEmptyKind } from "./feedEmpty.ts";

// A preview never reads "quiet" (it would say the room is empty of people
// when only the reader has not joined), whatever agent the room has.
const cases: Array<[Parameters<typeof feedEmptyKind>[0], string]> = [
  [{ people: true, preview: false, agentReady: false }, "people"],
  [{ people: false, preview: true, agentReady: false }, "preview"],
  [{ people: false, preview: true, agentReady: true }, "preview"],
  [{ people: false, preview: false, agentReady: true }, "ready"],
  [{ people: false, preview: false, agentReady: false }, "quiet"],
];
for (const [input, want] of cases) {
  if (feedEmptyKind(input) !== want) throw new Error(`${JSON.stringify(input)}: ${feedEmptyKind(input)}, want ${want}`);
}
