// Guided first-run tour + getting-started checklist (PRD 2026-10-10 display
// names + onboarding §3): the step machine, the event → checklist tick
// mapping, the per-member persisted state and the start conditions. Pure,
// node-tested; useOnboarding wires it to storage and the event bus, and
// views/onboarding renders it.
import type { OnboardingEvent } from "./onboardingEvents.ts";
import { gatewayBase } from "./config.ts";
import { StorageKey, memberKey } from "./storageKeys.ts";

type Storage = Pick<globalThis.Storage, "getItem" | "setItem">;

// ---- tour steps

// A step's `targets` are data-tour anchors in priority order: the first one
// on screen is spotlit (e.g. the newest deliverable card in the feed, else
// the Files tab). With none on screen the card
// shows centered with `fallback` (when set) instead of the step text.
export interface TourStep {
  readonly id: StepId;
  readonly title: string;
  readonly text: string;
  readonly targets: readonly string[];
  readonly fallback?: string;
  // "next": only the Next/Done button advances (the card traps focus).
  // "action": a real event advances; `button` (if any) is an alternative.
  readonly advance: "next" | "action";
  readonly button: "Next" | "Done" | null;
  // A short line under the text while the step waits for the real action.
  readonly waiting?: string;
}

export type StepId = "start" | "room" | "agent" | "ask" | "wait" | "results" | "share" | "invite";
export type TourId = "main" | "invite";

export const TOURS: Readonly<Record<TourId, readonly TourStep[]>> = {
  main: [
    {
      // On the home view (outside a room): the welcome card's start button.
      // Opening any room — from the card or another way — moves on.
      id: "start",
      title: "Start here",
      text: "One click creates your first room with an AI agent. Nothing to install.",
      targets: ["welcome-start"],
      advance: "action",
      button: null,
      waiting: "Click the button to begin",
    },
    {
      id: "room",
      title: "Room",
      text: "This is your room. People and agents work together here; everything stays in this conversation.",
      targets: ["room-header"],
      advance: "next",
      button: "Next",
    },
    {
      id: "agent",
      title: "Agent",
      text: "This is the agent you're talking to. It runs on a teammate's machine, so there's nothing to install.",
      targets: ["agent-picker"],
      advance: "next",
      button: "Next",
    },
    {
      id: "ask",
      title: "Ask",
      text: "Pick an example or write your own, then press Send.",
      targets: ["composer"],
      advance: "action",
      button: null,
      waiting: "Waiting for your message",
    },
    {
      id: "wait",
      title: "Wait",
      text: "The agent is working…",
      targets: ["feed"],
      advance: "action",
      button: null,
      waiting: "Its reply will appear here",
    },
    {
      id: "results",
      title: "Results",
      text: "Anything the agent makes (pages, docs, images) shows up here. Open it to preview.",
      targets: ["deliverable", "files"],
      fallback: "Anything the agent makes (pages, docs, images) shows up as a card in the conversation and under Files. Open it to preview.",
      advance: "action",
      button: "Next",
    },
    {
      id: "share",
      title: "Share",
      text: "Share a link to show your work.",
      targets: ["share", "files"],
      fallback: "Open a file from Files, then use Share in its toolbar to create a public link.",
      advance: "action",
      button: "Done",
    },
  ],
  invite: [
    {
      id: "invite",
      title: "Invite",
      text: "Invite a teammate: open the room settings to create an invite link or add a member.",
      targets: ["invite"],
      fallback: "Only a room's owner can invite. Start your own room to invite teammates, or ask the owner for an invite link.",
      advance: "action",
      button: "Done",
    },
  ],
};

// ---- checklist

export type ChecklistItemId = "send" | "reply" | "make" | "preview" | "share" | "invite";

export interface TourStart {
  readonly tour: TourId;
  readonly step: number;
}

const stepIndex = (id: StepId): number => {
  const at = TOURS.main.findIndex((s) => s.id === id);
  return at === -1 ? 0 : at;
};

// TOUR_IN_ROOM is where a tour restarted from the checklist begins: the room
// steps (the start step only makes sense on the home view).
export const TOUR_IN_ROOM: TourStart = { tour: "main", step: stepIndex("room") };

export const CHECKLIST: readonly { readonly id: ChecklistItemId; readonly label: string; readonly showMe: TourStart }[] = [
  { id: "send", label: "Send your first message", showMe: { tour: "main", step: stepIndex("ask") } },
  { id: "reply", label: "Get your first agent reply", showMe: { tour: "main", step: stepIndex("ask") } },
  { id: "make", label: "Ask the agent to make something", showMe: { tour: "main", step: stepIndex("ask") } },
  { id: "preview", label: "Open a preview", showMe: { tour: "main", step: stepIndex("results") } },
  { id: "share", label: "Share a link", showMe: { tour: "main", step: stepIndex("share") } },
  { id: "invite", label: "Invite a teammate", showMe: { tour: "invite", step: 0 } },
];

// checklistTick: the item an event ticks (every source counts — history
// pre-ticks what a member with existing activity already did).
export function checklistTick(e: OnboardingEvent): ChecklistItemId | null {
  switch (e.kind) {
    case "message_sent":
      return "send";
    case "agent_reply":
      return "reply";
    case "deliverable":
      return "make";
    case "artifact_opened":
      return "preview";
    case "share_created":
      return "share";
    case "invite_created":
      return "invite";
    case "room_opened":
      return null;
  }
}

// ---- state

export interface TourState {
  readonly status: "never" | "active" | "skipped" | "done";
  readonly tour: TourId;
  readonly step: number;
  // The sequence of the message sent at the Ask step: only an agent reply
  // after it finishes the Wait step (a replayed older reply never does).
  readonly sentSeq: number | null;
}

export interface OnboardingState {
  readonly done: Readonly<Partial<Record<ChecklistItemId, number>>>;
  readonly tour: TourState;
  readonly collapsed: boolean;
  readonly celebrated: boolean;
  readonly dismissed: boolean;
}

export const INITIAL_ONBOARDING: OnboardingState = {
  done: {},
  tour: { status: "never", tour: "main", step: 0, sentSeq: null },
  collapsed: false,
  celebrated: false,
  dismissed: false,
};

export type OnboardingAction =
  | { readonly type: "event"; readonly event: OnboardingEvent; readonly at: number }
  | { readonly type: "start"; readonly start: TourStart }
  | { readonly type: "next" }
  | { readonly type: "skip" }
  | { readonly type: "collapse"; readonly collapsed: boolean }
  | { readonly type: "celebrated" }
  | { readonly type: "dismiss" };

export function currentStep(t: TourState): TourStep | null {
  if (t.status !== "active") return null;
  return TOURS[t.tour][t.step] ?? null;
}

function advanceTo(t: TourState, step: number): TourState {
  return step >= TOURS[t.tour].length ? { ...t, status: "done", step: 0, sentSeq: null } : { ...t, step };
}

// reduceTour: the step machine. Next only moves a step that offers a button;
// real events move the step that waits for them (sending early, during the
// Room/Agent steps, jumps straight to Wait). Esc/Skip ends the tour.
export function reduceTour(t: TourState, action: OnboardingAction): TourState {
  if (action.type === "start") {
    const steps = TOURS[action.start.tour];
    const step = Math.min(Math.max(0, action.start.step), steps.length - 1);
    return { status: "active", tour: action.start.tour, step, sentSeq: null };
  }
  const step = currentStep(t);
  if (!step) return t;
  if (action.type === "skip") return { ...t, status: "skipped", sentSeq: null };
  if (action.type === "next") return step.button ? advanceTo(t, t.step + 1) : t;
  if (action.type !== "event") return t;
  const e = action.event;
  if (t.tour === "invite") return e.kind === "invite_created" ? advanceTo(t, t.step + 1) : t;
  const at = (id: StepId) => stepIndex(id);
  switch (e.kind) {
    case "room_opened":
      return t.step === at("start") ? advanceTo(t, at("room")) : t;
    case "message_sent":
      if (e.source === "action" && e.toAgent && t.step <= at("ask")) return { ...t, step: at("wait"), sentSeq: e.seq ?? null };
      return t;
    case "agent_reply": {
      if (t.step !== at("wait") || e.source === "history") return t;
      const fresh = t.sentSeq == null || e.seq == null || e.seq > t.sentSeq;
      return fresh ? { ...advanceTo(t, at("results")), sentSeq: null } : t;
    }
    case "artifact_opened":
      return t.step === at("results") ? advanceTo(t, at("share")) : t;
    case "share_created":
      return t.step >= at("results") ? advanceTo(t, TOURS.main.length) : t;
    default:
      return t;
  }
}

export function reduceOnboarding(s: OnboardingState, action: OnboardingAction): OnboardingState {
  switch (action.type) {
    case "event": {
      const tick = checklistTick(action.event);
      const done = tick && !s.done[tick] ? { ...s.done, [tick]: action.at } : s.done;
      const tour = reduceTour(s.tour, action);
      return done === s.done && tour === s.tour ? s : { ...s, done, tour };
    }
    case "collapse":
      return s.collapsed === action.collapsed ? s : { ...s, collapsed: action.collapsed };
    case "celebrated":
      return s.celebrated ? s : { ...s, celebrated: true };
    case "dismiss":
      return s.dismissed ? s : { ...s, dismissed: true };
    default: {
      const tour = reduceTour(s.tour, action);
      return tour === s.tour ? s : { ...s, tour };
    }
  }
}

// ---- checklist view

export type ChecklistPhase = "list" | "celebrate" | "chip" | "hidden";

export function doneCount(s: OnboardingState): number {
  return CHECKLIST.filter((i) => s.done[i.id]).length;
}

// checklistPhase: the list until all six are done, then one celebration,
// then the "You're set" chip until it is dismissed.
export function checklistPhase(s: OnboardingState): ChecklistPhase {
  if (s.dismissed) return "hidden";
  if (doneCount(s) < CHECKLIST.length) return "list";
  return s.celebrated ? "chip" : "celebrate";
}

// ---- start conditions

// shouldAutoStartTour: the tour starts by itself only for a brand-new member
// on their first visit (no rooms, nothing done yet, never shown before), at
// the start step on the welcome card. Anyone else starts it from the
// checklist.
export function shouldAutoStartTour(s: OnboardingState, priorRoomCount: number | null): boolean {
  return priorRoomCount === 0 && s.tour.status === "never" && doneCount(s) === 0;
}

// ---- persistence

export const onboardingKey = (subject: string) => memberKey(StorageKey.onboarding, { gateway: gatewayBase(), subject });

const ITEM_IDS = new Set<string>(CHECKLIST.map((i) => i.id));
const STATUSES = new Set<string>(["never", "active", "skipped", "done"]);

// parseOnboarding reads a stored state defensively: unknown or malformed
// fields fall back to their initial value, so a corrupt entry never breaks
// the shell.
export function parseOnboarding(raw: string | null): OnboardingState {
  if (!raw) return INITIAL_ONBOARDING;
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return INITIAL_ONBOARDING;
  }
  if (!v || typeof v !== "object") return INITIAL_ONBOARDING;
  const o = v as Record<string, unknown>;
  const done: Partial<Record<ChecklistItemId, number>> = {};
  if (o.done && typeof o.done === "object") {
    for (const [k, at] of Object.entries(o.done as Record<string, unknown>)) {
      if (ITEM_IDS.has(k) && typeof at === "number" && Number.isFinite(at)) done[k as ChecklistItemId] = at;
    }
  }
  const t = (o.tour && typeof o.tour === "object" ? o.tour : {}) as Record<string, unknown>;
  const tourId: TourId = t.tour === "invite" ? "invite" : "main";
  const step = typeof t.step === "number" && Number.isInteger(t.step) && t.step >= 0 && t.step < TOURS[tourId].length ? t.step : 0;
  return {
    done,
    tour: {
      status: typeof t.status === "string" && STATUSES.has(t.status) ? (t.status as TourState["status"]) : "never",
      tour: tourId,
      step,
      sentSeq: typeof t.sentSeq === "number" && Number.isFinite(t.sentSeq) ? t.sentSeq : null,
    },
    collapsed: o.collapsed === true,
    celebrated: o.celebrated === true,
    dismissed: o.dismissed === true,
  };
}

export function loadOnboarding(storage: Storage | null, subject: string | null): OnboardingState {
  if (!storage || !subject) return INITIAL_ONBOARDING;
  try {
    return parseOnboarding(storage.getItem(onboardingKey(subject)));
  } catch {
    return INITIAL_ONBOARDING;
  }
}

export function saveOnboarding(storage: Storage | null, subject: string | null, s: OnboardingState): void {
  if (!storage || !subject) return;
  try {
    storage.setItem(onboardingKey(subject), JSON.stringify(s));
  } catch {
    // private mode / full storage: progress lives for this tab only
  }
}
