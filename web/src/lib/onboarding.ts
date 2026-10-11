// First-run onboarding (docs/prd/2026-10-10-platform-agents-v2-onboarding.md
// §1): the home views' welcome card and the empty room's starter prompts.
// Pure logic, node-tested; the views live in views/WelcomeCard.tsx and
// views/composer/StarterPrompts.tsx.
import { platformChoices, type PlatformAgent, type PlatformChoice } from "./platformAgents.ts";

type Storage = Pick<globalThis.Storage, "getItem" | "setItem">;

const dismissKey = (subject: string) => `bnbloc.welcomeDismissed.${subject.toLowerCase()}`;

export function welcomeDismissed(storage: Storage | null, subject: string | null): boolean {
  if (!storage || !subject) return false;
  try {
    return storage.getItem(dismissKey(subject)) === "1";
  } catch {
    return false;
  }
}

export function dismissWelcome(storage: Storage | null, subject: string | null): void {
  if (!storage || !subject) return;
  try {
    storage.setItem(dismissKey(subject), "1");
  } catch {
    // private mode: the card simply shows again next visit
  }
}

// showWelcome: a member with no rooms always sees the welcome card (it is
// their way in); a member with rooms sees it until they dismiss it. Nothing
// shows until the room list is known (roomCount null), so it never flashes.
export function showWelcome(s: { readonly roomCount: number | null; readonly dismissed: boolean }): boolean {
  if (s.roomCount === null) return false;
  return s.roomCount === 0 || !s.dismissed;
}

// The welcome card's desktop-app action, from the app-wide desktop presence:
// inside the desktop app ("shell") there is nothing to install; a browser
// member whose app is running ("connected") is told so; one whose app
// connected before but is closed is offered to open it ("open"), and the
// download only once opening it failed; one whose app never connected is
// offered the install. Until the first presence check answers (installed
// undefined), and where desktop guidance does not apply (a phone), nothing is
// offered, so the wrong button never flashes.
export type WelcomeDesktop = "shell" | "connected" | "open" | "install" | "none";

export function welcomeDesktopAction(p: {
  readonly inShell: boolean;
  readonly applicable: boolean;
  readonly connected: boolean;
  readonly installed: boolean | undefined;
  readonly openFailed: boolean;
}): WelcomeDesktop {
  if (p.inShell) return "shell";
  if (!p.applicable || p.installed === undefined) return "none";
  if (p.connected) return "connected";
  if (p.installed && !p.openFailed) return "open";
  return "install";
}

// The sentence the card adds, after "no platform agent", for each action.
export const WELCOME_DESKTOP_HINT: Readonly<Record<WelcomeDesktop, string>> = {
  shell: "You can also connect your own agent core from the Agents page.",
  connected: "Your desktop app is connected: open a room and pick one of your own agents.",
  open: "Your desktop app isn't running: open it to use your own agents.",
  install: "Meanwhile, install the desktop app to use your own agents.",
  none: "",
};

// The welcome card may be dismissed only once the member has a room.
export function canDismissWelcome(roomCount: number | null): boolean {
  return (roomCount ?? 0) > 0;
}

export const FIRST_ROOM_NAME = "My first room";

// firstRoomName de-duplicates "My first room" against the member's rooms:
// "My first room", then "My first room 2", "My first room 3"…
export function firstRoomName(existing: readonly string[]): string {
  const taken = new Set(existing.map((n) => n.trim().toLowerCase()));
  if (!taken.has(FIRST_ROOM_NAME.toLowerCase())) return FIRST_ROOM_NAME;
  for (let i = 2; ; i++) {
    const name = `${FIRST_ROOM_NAME} ${i}`;
    if (!taken.has(name.toLowerCase())) return name;
  }
}

// firstRoomId is the new room's id. Room ids are global, so it carries a
// random suffix (the caller retries with a fresh one on a 409).
export function firstRoomId(random: () => number = Math.random): string {
  const alphabet = "abcdefghijkmnpqrstuvwxyz23456789";
  let suffix = "";
  for (let i = 0; i < 6; i++) suffix += alphabet[Math.floor(random() * alphabet.length) % alphabet.length];
  return `my-first-room-${suffix}`;
}

// defaultPlatformChoice is the welcome card's agent: the first published
// agent with an online host that is not the member themselves (null: none
// available, the card offers the desktop path only).
export function defaultPlatformChoice(offered: readonly PlatformAgent[], subject: string | null): PlatformChoice | null {
  return platformChoices(offered, [], subject).find((c) => c.online) ?? null;
}

export interface StarterPrompt {
  readonly label: string;
  readonly text: string;
}

// The empty room's example prompts: each shows a part of the product (a
// previewable web app, a Markdown deliverable, a quick answer, a diagram).
export const STARTER_PROMPTS: readonly StarterPrompt[] = [
  { label: "Build a landing page", text: "Build a one-page landing page for a coffee shop" },
  { label: "Write a project plan", text: "Write a project plan for launching a community podcast as a Markdown doc" },
  { label: "Explain an idea", text: "Explain this idea in 5 bullet points: " },
  { label: "Draw a diagram", text: "Draw a simple diagram of how an online order reaches the kitchen" },
];

// showStarterPrompts: only on the room composer of an empty feed (nothing
// sent, nothing working) with an agent to send to. They vanish with the
// first message.
export function showStarterPrompts(s: {
  readonly feedLength: number;
  readonly working: number;
  readonly agentAvailable: boolean;
  readonly people: boolean;
}): boolean {
  return !s.people && s.agentAvailable && s.feedLength === 0 && s.working === 0;
}
