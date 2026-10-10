import {
  canDismissWelcome,
  defaultPlatformChoice,
  dismissWelcome,
  firstRoomId,
  firstRoomName,
  showStarterPrompts,
  showWelcome,
  STARTER_PROMPTS,
  welcomeDismissed,
} from "./onboarding.ts";
import type { PlatformAgent } from "./platformAgents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

function memoryStorage(): Pick<Storage, "getItem" | "setItem"> {
  const data = new Map<string, string>();
  return { getItem: (k) => data.get(k) ?? null, setItem: (k, v) => void data.set(k, v) };
}

function testWelcomeVisibility(): void {
  assert(!showWelcome({ roomCount: null, dismissed: false }), "nothing until the room list is known");
  assert(showWelcome({ roomCount: 0, dismissed: false }), "no rooms: shown");
  assert(showWelcome({ roomCount: 0, dismissed: true }), "no rooms: shown even after a dismissal");
  assert(showWelcome({ roomCount: 3, dismissed: false }), "rooms, not dismissed: shown");
  assert(!showWelcome({ roomCount: 3, dismissed: true }), "dismissed: hidden");
  assert(!canDismissWelcome(0) && canDismissWelcome(1), "dismissable once the member has a room");
}

function testDismissalIsPerUser(): void {
  const s = memoryStorage();
  dismissWelcome(s, "Jack");
  assert(welcomeDismissed(s, "jack"), "dismissed for jack");
  assert(!welcomeDismissed(s, "rose"), "not for rose");
  assert(!welcomeDismissed(null, "jack") && !welcomeDismissed(s, null), "no storage / no user");
}

function testFirstRoomNaming(): void {
  assert(firstRoomName([]) === "My first room", "plain name");
  assert(firstRoomName(["my first room"]) === "My first room 2", "de-duplicated");
  assert(firstRoomName(["My first room", "My first room 2"]) === "My first room 3", "next free number");
  const id = firstRoomId(() => 0);
  assert(/^my-first-room-[a-z2-9]{6}$/.test(id), `id shape: ${id}`);
  assert(firstRoomId(() => 0.999999) !== id, "the suffix varies");
}

function testDefaultPlatformAgent(): void {
  const host = (hostUserId: string, online: boolean) => ({ hostUserId, online, queued: 0, targetId: "" });
  const offered: PlatformAgent[] = [
    { core: "nimbus", provider: "claude-code", online: false, hosts: [host("admin", false)] },
    { core: "nimbus", provider: "codex", online: true, hosts: [host("admin", true)] },
  ];
  assert(defaultPlatformChoice(offered, "jack")?.agent.provider === "codex", "the first agent with an online host");
  assert(defaultPlatformChoice(offered, "admin") === null, "never the member's own hosting");
  assert(defaultPlatformChoice([], "jack") === null, "platform agents off");
}

function testStarterPrompts(): void {
  assert(STARTER_PROMPTS.length >= 3 && STARTER_PROMPTS.length <= 4, "3–4 prompts");
  const base = { feedLength: 0, working: 0, agentAvailable: true, people: false };
  assert(showStarterPrompts(base), "an empty feed with an agent");
  assert(!showStarterPrompts({ ...base, feedLength: 1 }), "gone after the first message");
  assert(!showStarterPrompts({ ...base, working: 1 }), "not while an agent works");
  assert(!showStarterPrompts({ ...base, agentAvailable: false }), "no agent to send to");
  assert(!showStarterPrompts({ ...base, people: true }), "never in the people chat");
}

testWelcomeVisibility();
testDismissalIsPerUser();
testFirstRoomNaming();
testDefaultPlatformAgent();
testStarterPrompts();
