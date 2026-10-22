import { onboardingEventFromRoomEvent, onboardingEvents, tapOnboarding, type OnboardingEvent } from "./onboardingEvents.ts";
import type { RoomEvent } from "./roomEvents.ts";
import {
  CHECKLIST,
  checklistPhase,
  checklistTick,
  currentStep,
  doneCount,
  INITIAL_ONBOARDING,
  loadOnboarding,
  onboardingKey,
  parseOnboarding,
  reduceOnboarding,
  saveOnboarding,
  shouldAutoStartTour,
  TOUR_IN_ROOM,
  TOURS,
  type OnboardingAction,
  type OnboardingState,
} from "./onboardingTour.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function memoryStorage(): Pick<Storage, "getItem" | "setItem"> & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return { data, getItem: (k) => data.get(k) ?? null, setItem: (k, v) => void data.set(k, v) };
}

const ROOM = "my-first-room-abc123";
const ev = (event: OnboardingEvent): OnboardingAction => ({ type: "event", event, at: 1000 });
const run = (s: OnboardingState, ...actions: OnboardingAction[]) => actions.reduce(reduceOnboarding, s);
const stepId = (s: OnboardingState) => currentStep(s.tour)?.id ?? s.tour.status;
const idx = (id: string) => TOURS.main.findIndex((s) => s.id === id);
const started = reduceOnboarding(INITIAL_ONBOARDING, { type: "start", start: TOUR_IN_ROOM });

function testStepList(): void {
  assert(TOURS.main.map((s) => s.id).join(",") === "start,room,agent,ask,wait,results,share", "the seven steps in order");
  const targets = TOURS.main.map((s) => s.targets[0]).join(",");
  assert(targets === "welcome-start,room-header,agent-picker,composer,feed,deliverable,share", `data-tour targets: ${targets}`);
  assert(TOURS.main[idx("results")].targets.includes("files") && TOURS.main[idx("share")].targets.includes("files"), "the Files tab is the fallback anchor");
  assert(TOURS.main[idx("start")].advance === "action" && TOURS.main[idx("start")].button === null, "Start waits for a room to open");
  assert(TOURS.main[idx("room")].advance === "next" && TOURS.main[idx("agent")].advance === "next", "Room and Agent advance on Next");
  assert(TOURS.main[idx("ask")].button === null && TOURS.main[idx("wait")].button === null, "Ask and Wait have no Next: the real action moves them");
  assert(TOURS.main[idx("share")].button === "Done", "Share ends with Done");
  assert(TOUR_IN_ROOM.step === idx("room"), "a restart from the checklist begins at the room steps");
}

function testStartStep(): void {
  let s = reduceOnboarding(INITIAL_ONBOARDING, { type: "start", start: { tour: "main", step: 0 } });
  assert(stepId(s) === "start", "a brand-new member's tour opens on the start button");
  s = run(s, { type: "next" });
  assert(stepId(s) === "start", "Next does nothing at Start: opening a room moves on");
  s = run(s, ev({ kind: "room_opened", roomId: ROOM }));
  assert(stepId(s) === "room", "opening any room moves to the room steps");
  s = run(s, ev({ kind: "room_opened", roomId: "other" }));
  assert(stepId(s) === "room", "opening a room later never moves a room step");
  assert(checklistTick({ kind: "room_opened", roomId: ROOM }) === null, "opening a room ticks nothing");
}

function testHappyPath(): void {
  let s = run(started, { type: "next" }, { type: "next" });
  assert(stepId(s) === "ask", "Next, Next reaches Ask");
  s = run(s, { type: "next" });
  assert(stepId(s) === "ask", "Next does nothing at Ask");
  s = run(s, ev({ kind: "message_sent", roomId: ROOM, source: "stream", seq: 7, toAgent: true }));
  assert(stepId(s) === "ask", "a stream echo (maybe a replay) does not advance Ask");
  s = run(s, ev({ kind: "message_sent", roomId: ROOM, source: "action", seq: 7, toAgent: true }));
  assert(stepId(s) === "wait" && s.tour.sentSeq === 7, "the member's send moves to Wait and remembers its seq");
  s = run(s, ev({ kind: "agent_reply", roomId: ROOM, source: "stream", seq: 6 }));
  assert(stepId(s) === "wait", "an older (replayed) reply does not finish Wait");
  s = run(s, ev({ kind: "agent_reply", roomId: ROOM, source: "history", seq: 9 }));
  assert(stepId(s) === "wait", "a history row does not finish Wait");
  s = run(s, ev({ kind: "agent_reply", roomId: ROOM, source: "stream", seq: 8 }));
  assert(stepId(s) === "results" && s.tour.sentSeq === null, "the first fresh reply moves to Results");
  s = run(s, ev({ kind: "deliverable", roomId: ROOM, source: "stream" }));
  assert(stepId(s) === "results", "a deliverable appearing ticks the checklist, the step waits for the open");
  s = run(s, ev({ kind: "artifact_opened", roomId: ROOM }));
  assert(stepId(s) === "share", "opening a deliverable moves to Share");
  s = run(s, ev({ kind: "share_created", roomId: ROOM }));
  assert(s.tour.status === "done", "creating a link finishes the tour");
  assert(doneCount(s) === 5, `items 1–5 ticked along the way: ${doneCount(s)}`);
}

function testNextAndEarlyActions(): void {
  let s = run(started, ev({ kind: "message_sent", roomId: ROOM, source: "action", seq: 3, toAgent: true }));
  assert(stepId(s) === "wait", "sending during Room jumps straight to Wait");
  s = run(started, ev({ kind: "message_sent", roomId: ROOM, source: "action", seq: 3, toAgent: false }));
  assert(stepId(s) === "room", "a people-only message brings no reply: no jump");
  s = run(started, { type: "next" }, { type: "next" }, ev({ kind: "message_sent", roomId: ROOM, source: "action", toAgent: true }));
  s = run(s, ev({ kind: "agent_reply", roomId: ROOM, source: "stream" }));
  assert(stepId(s) === "results", "without sequences any live reply finishes Wait");
  s = run(s, ev({ kind: "artifact_opened", roomId: ROOM }), { type: "next" });
  assert(s.tour.status === "done", "Done on Share ends the tour");
  s = run(started, { type: "next" }, { type: "next" }, ev({ kind: "artifact_opened", roomId: ROOM }));
  assert(stepId(s) === "ask", "opening a file early never skips the Ask step");
  s = reduceOnboarding(INITIAL_ONBOARDING, { type: "start", start: { tour: "main", step: idx("results") } });
  s = run(s, { type: "next" });
  assert(stepId(s) === "share", "Next on Results moves on without opening anything");
}

function testSkipAndRestart(): void {
  let s = run(started, { type: "next" }, { type: "skip" });
  assert(s.tour.status === "skipped" && currentStep(s.tour) === null, "Skip ends the tour");
  s = run(s, { type: "next" }, ev({ kind: "message_sent", roomId: ROOM, source: "action", toAgent: true }));
  assert(s.tour.status === "skipped", "a skipped tour ignores Next and events");
  s = run(s, { type: "start", start: TOUR_IN_ROOM });
  assert(stepId(s) === "room", "Take the tour restarts from the room steps");
  s = run(s, { type: "start", start: { tour: "main", step: 99 } });
  assert(stepId(s) === "share", "a start step is clamped to the tour");
  s = run(s, { type: "start", start: { tour: "invite", step: 0 } });
  assert(stepId(s) === "invite", "the invite spotlight");
  s = run(s, ev({ kind: "share_created", roomId: ROOM }));
  assert(stepId(s) === "invite", "the invite step waits for an invite");
  s = run(s, ev({ kind: "invite_created", roomId: ROOM }));
  assert(s.tour.status === "done", "an invite finishes it");
}

function testChecklistTicks(): void {
  const cases: [OnboardingEvent, string][] = [
    [{ kind: "message_sent", roomId: ROOM, source: "history", toAgent: true }, "send"],
    [{ kind: "agent_reply", roomId: ROOM, source: "stream" }, "reply"],
    [{ kind: "deliverable", roomId: ROOM, source: "history" }, "make"],
    [{ kind: "artifact_opened", roomId: ROOM }, "preview"],
    [{ kind: "share_created", roomId: ROOM }, "share"],
    [{ kind: "invite_created", roomId: ROOM }, "invite"],
  ];
  for (const [e, id] of cases) assert(checklistTick(e) === id, `${e.kind} ticks ${id}`);
  assert(CHECKLIST.map((i) => i.id).join(",") === "send,reply,make,preview,share,invite", "six items in PRD order");
  let s = run(INITIAL_ONBOARDING, ev(cases[0][0]));
  const first = s.done.send;
  s = run(s, { type: "event", event: cases[0][0], at: 5000 });
  assert(s.done.send === first, "a tick keeps its first time");
  assert(run(s, ev(cases[0][0])) === s, "a repeated tick is a no-op (no re-save)");
  assert(checklistPhase(s) === "list", "list until all six");
  for (const [e] of cases) s = run(s, ev(e));
  assert(doneCount(s) === 6 && checklistPhase(s) === "celebrate", "all six: celebrate");
  s = run(s, { type: "celebrated" });
  assert(checklistPhase(s) === "chip", "then the You're set chip");
  s = run(s, { type: "dismiss" });
  assert(checklistPhase(s) === "hidden", "dismissed: gone");
  const showMe = Object.fromEntries(CHECKLIST.map((i) => [i.id, `${i.showMe.tour}:${TOURS[i.showMe.tour][i.showMe.step].id}`]));
  assert(showMe.send === "main:ask" && showMe.preview === "main:results" && showMe.share === "main:share", "Show me → tour step");
  assert(showMe.invite === "invite:invite", "Show me for invites → the invite spotlight");
}

function testRoomEventMapping(): void {
  const chat: RoomEvent = { kind: "chat_message", text: "hi", author: "jack", sequence: 4, toAgent: true };
  const own = onboardingEventFromRoomEvent(chat, "jack", ROOM, "stream");
  assert(own?.kind === "message_sent" && own.seq === 4 && own.toAgent, "own echo is a send");
  assert(onboardingEventFromRoomEvent({ ...chat, author: "Jack" }, "jack", ROOM, "stream") === null, "Jack is another member");
  assert(onboardingEventFromRoomEvent({ ...chat, author: "rose" }, "jack", ROOM, "stream") === null, "someone else's message is nothing");
  const people = onboardingEventFromRoomEvent({ ...chat, toAgent: false }, "jack", ROOM, "history");
  assert(people?.kind === "message_sent" && !people.toAgent && people.source === "history", "people-only send");
  const reply = onboardingEventFromRoomEvent({ kind: "agent_messages", text: "done", author: "agent", sequence: 5 }, "jack", ROOM, "stream");
  assert(reply?.kind === "agent_reply" && reply.seq === 5, "assistant message is a reply");
  assert(onboardingEventFromRoomEvent({ kind: "agent_messages", text: "", author: "agent" }, "jack", ROOM, "stream") === null, "empty is nothing");
  assert(onboardingEventFromRoomEvent({ kind: "artifact_updated" }, "jack", ROOM, "stream")?.kind === "deliverable", "artifact_updated is a deliverable");
  assert(onboardingEventFromRoomEvent({ kind: "presence_join" }, "jack", ROOM, "stream") === null, "other kinds are nothing");
}

async function testBus(): Promise<void> {
  const seen: string[] = [];
  const off = onboardingEvents.subscribe((e) => seen.push(e.kind));
  const bad = onboardingEvents.subscribe(() => {
    throw new Error("consumer bug");
  });
  onboardingEvents.emit({ kind: "share_created", roomId: ROOM });
  onboardingEvents.emit(null);
  const value = await Promise.resolve({ code: "x" }).then(tapOnboarding({ kind: "invite_created", roomId: ROOM }));
  assert(value.code === "x", "tap passes the value through");
  off();
  bad();
  onboardingEvents.emit({ kind: "share_created", roomId: ROOM });
  assert(seen.join(",") === "share_created,invite_created", `delivered once each, despite a throwing consumer: ${seen}`);
}

function testPersistence(): void {
  const st = memoryStorage();
  const s = run(started, { type: "next" }, ev({ kind: "invite_created", roomId: ROOM }), { type: "collapse", collapsed: true });
  saveOnboarding(st, "jack", s);
  assert(
    st.data.has("bnbloc.onboarding.v1:same-origin:jack") && onboardingKey("JACK") === "bnbloc.onboarding.v1:same-origin:JACK",
    "per-member key on this gateway (exact subject)",
  );
  const back = loadOnboarding(st, "jack");
  assert(JSON.stringify(back) === JSON.stringify(s), "round-trips");
  assert(loadOnboarding(st, "rose") === INITIAL_ONBOARDING, "another member starts fresh");
  assert(loadOnboarding(null, "jack") === INITIAL_ONBOARDING && loadOnboarding(st, null) === INITIAL_ONBOARDING, "no storage / no user");
  assert(parseOnboarding("{nope") === INITIAL_ONBOARDING, "corrupt JSON");
  const odd = parseOnboarding(JSON.stringify({ done: { send: 1, bogus: 2, reply: "x" }, tour: { status: "weird", step: 42, tour: "main" }, collapsed: "yes" }));
  assert(odd.done.send === 1 && Object.keys(odd.done).length === 1, "only known items with numeric times");
  assert(odd.tour.status === "never" && odd.tour.step === 0 && !odd.collapsed, "malformed fields fall back");
  const failing = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("full"); } };
  assert(loadOnboarding(failing, "jack") === INITIAL_ONBOARDING, "a throwing storage reads as fresh");
  saveOnboarding(failing, "jack", s);
}

function testStartConditions(): void {
  assert(shouldAutoStartTour(INITIAL_ONBOARDING, 0), "brand-new member, first room from the welcome card");
  assert(!shouldAutoStartTour(INITIAL_ONBOARDING, 2), "a member who already had rooms");
  assert(!shouldAutoStartTour(INITIAL_ONBOARDING, null), "room count unknown");
  const active = run(INITIAL_ONBOARDING, ev({ kind: "agent_reply", roomId: ROOM, source: "history" }));
  assert(!shouldAutoStartTour(active, 0), "existing activity: never auto-starts");
  assert(!shouldAutoStartTour(run(started, { type: "skip" }), 0), "seen (and skipped) before: never again by itself");
}

testStepList();
testStartStep();
testHappyPath();
testNextAndEarlyActions();
testSkipAndRestart();
testChecklistTicks();
testRoomEventMapping();
await testBus();
testPersistence();
testStartConditions();
