import {
  canDismissWelcome,
  defaultPlatformChoice,
  dismissWelcome,
  firstRoomId,
  firstRoomName,
  showStarterPrompts,
  showWelcome,
  STARTER_PROMPTS,
  WELCOME_DESKTOP_HINT,
  welcomeDesktopAction,
  welcomeStart,
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
  dismissWelcome(s, "jack");
  assert(welcomeDismissed(s, "jack"), "dismissed for jack");
  assert(!welcomeDismissed(s, "Jack"), "Jack is another member");
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

// Regression (docs/bugs/2026-10-11-welcome-card-install-prompt-ignores-desktop.md):
// the card offered the install to members whose app was installed, running
// or not.
function testWelcomeDesktopAction(): void {
  const browser = { inShell: false, applicable: true, connected: false, installed: false, openFailed: false };
  assert(welcomeDesktopAction({ ...browser, inShell: true }) === "shell", "inside the desktop app");
  assert(welcomeDesktopAction({ ...browser, installed: undefined }) === "none", "nothing until the check answers");
  assert(welcomeDesktopAction({ ...browser, applicable: false }) === "none", "a phone gets no desktop action");
  assert(welcomeDesktopAction({ ...browser, connected: true, installed: true }) === "connected", "a running app: no install");
  assert(welcomeDesktopAction({ ...browser, installed: true }) === "open", "installed but closed: open it");
  assert(welcomeDesktopAction({ ...browser, installed: true, openFailed: true }) === "install", "opening failed: offer the download");
  assert(welcomeDesktopAction(browser) === "install", "never connected: install");
  assert(WELCOME_DESKTOP_HINT.none === "" && !WELCOME_DESKTOP_HINT.connected.includes("install"), "hints");
}

// Regression (docs/bugs/2026-10-11-welcome-card-hides-self-hosted-agents.md):
// a member hosting every platform agent was told none is available.
function testWelcomeStart(): void {
  const host = (id: string, online: boolean) => ({ hostUserId: id, online, queued: 0 });
  const mine = [
    { core: "acme", provider: "codex", online: true, hosts: [host("maya", true)] },
    { core: "acme", provider: "claude-code", online: true, hosts: [host("maya", true)] },
  ] as unknown as PlatformAgent[];
  const own = [
    { core: "acme", provider: "opencode", online: true },
    { core: "acme", provider: "claude-code", online: true },
  ];
  const start = welcomeStart(mine, own, "maya");
  assert(start.kind === "own" && start.agent.provider === "claude-code", "the host starts with their own published agent");
  const other = [{ core: "acme", provider: "codex", online: true, hosts: [host("jack", true)] }] as unknown as PlatformAgent[];
  assert(welcomeStart(other, own, "maya").kind === "platform", "another member's online host is offered first");
  const unpublishedOwn = welcomeStart([], own, "maya");
  assert(unpublishedOwn.kind === "own" && unpublishedOwn.agent.provider === "opencode", "any own online agent when nothing is published");
  const offline = welcomeStart([{ core: "acme", provider: "codex", online: false, hosts: [host("jack", false)] }] as unknown as PlatformAgent[], [], "maya");
  assert(offline.kind === "none" && offline.reason === "offline", "published but no host online");
  const none = welcomeStart([], [{ core: "acme", provider: "codex", online: false }], "maya");
  assert(none.kind === "none" && none.reason === "unpublished", "nothing published, own agent offline");
}

testWelcomeVisibility();
testDismissalIsPerUser();
testFirstRoomNaming();
testDefaultPlatformAgent();
testStarterPrompts();
testWelcomeDesktopAction();
testWelcomeStart();
