import { applyLiveFrame, endLive, endTurnLive, frameKey, keyInput, liveList, pointAt, waitingFor } from "./jobLive.ts";
import type { JobLiveFrame } from "./wire.gen.ts";

// Local runner (the web tsconfig has no node types): a failure throws.
function eq(got: unknown, want: unknown, what: string): void {
  if (JSON.stringify(got) !== JSON.stringify(want)) throw new Error(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

const frame = (seq: number, extra: Partial<JobLiveFrame> = {}): JobLiveFrame => ({
  jobId: "j1", run: "r1", seq, width: 800, height: 600, owner: "maya", atMs: seq, ...extra,
});

// Newest wins; an older frame of the same run never replaces it; a control
// change (same seq) does; a new run starts over.
{
  let m = applyLiveFrame({}, frame(2));
  m = applyLiveFrame(m, frame(1));
  eq(m.j1?.seq, 2, "an older frame is ignored");
  m = applyLiveFrame(m, frame(2, { controller: "maya" }));
  eq(m.j1?.controller, "maya", "a control change at the same seq applies");
  m = applyLiveFrame(m, frame(1, { run: "r2" }));
  eq([m.j1?.run, m.j1?.seq], ["r2", 1], "a new run starts over");
  eq(frameKey(m.j1!), "r2:1", "frame key");
  eq(Object.keys(endLive(m, "j1")), [], "ended");
  eq(endLive(m, "other") === m, true, "ending another job keeps the map");
}

// A view waiting for a person outlives its turn; an ended view goes; a
// late viewer's list fills the map; the owner's waiting view is found in
// the channel shown.
{
  let m = applyLiveFrame({}, frame(2, { waiting: "Finish signing in", channelId: "ch-1" }));
  eq(Object.keys(endTurnLive(m, "j1")), ["j1"], "a waiting view stays after its turn");
  eq(Object.keys(endTurnLive(applyLiveFrame({}, frame(2)), "j1")), [], "a plain view ends with its turn");
  eq(waitingFor(m, "maya", "ch-1")?.jobId, "j1", "the owner's waiting view");
  eq(waitingFor(m, "jack", "ch-1"), undefined, "not another member's");
  eq(waitingFor(m, "maya", "ch-2"), undefined, "not in another channel");
  m = applyLiveFrame(m, frame(2, { ended: true }));
  eq(Object.keys(m), [], "an ended view goes");
  m = liveList({}, [frame(3, { jobId: "j2" }), frame(1)]);
  eq(Object.keys(m).sort(), ["j1", "j2"], "the room's list");
}

// Pointer mapping honours the letterbox of object-fit: contain.
{
  const box = { left: 0, top: 0, width: 1000, height: 1000 };
  const f = { width: 800, height: 400 }; // drawn 1000x500, 250px bars top and bottom
  eq(pointAt(box, f, 500, 500), { x: 0.5, y: 0.5 }, "centre");
  eq(pointAt(box, f, 0, 250), { x: 0, y: 0 }, "top-left corner of the picture");
  eq(pointAt(box, f, 500, 100), null, "the letterbox is not the page");
}

// Keys: a printable key types its text; a shortcut does not.
{
  const base = { altKey: false, ctrlKey: false, metaKey: false, shiftKey: false };
  eq(keyInput("down", { ...base, key: "a", code: "KeyA" }), { type: "key", action: "down", key: "a", code: "KeyA", text: "a", modifiers: 0 }, "printable");
  eq(keyInput("down", { ...base, ctrlKey: true, key: "a", code: "KeyA" }).text, undefined, "a shortcut types nothing");
  eq(keyInput("down", { ...base, key: "Enter", code: "Enter" }).text, undefined, "a named key types nothing");
}
