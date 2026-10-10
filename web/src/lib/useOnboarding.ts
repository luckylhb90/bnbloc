import { useSyncExternalStore } from "react";
import { onboardingEvents } from "@/lib/onboardingEvents";
import {
  INITIAL_ONBOARDING,
  loadOnboarding,
  onboardingKey,
  reduceOnboarding,
  saveOnboarding,
  shouldAutoStartTour,
  type OnboardingAction,
  type OnboardingState,
} from "@/lib/onboardingTour";
import { browserStorage } from "@/lib/platformRouting";

// The signed-in member's onboarding state (tour + checklist): one in-tab
// store, persisted per member in localStorage (onboardingKey) and fed by the
// onboarding event bus. The checklist and the tour overlay read it with
// useOnboarding; producers only emit onto the bus.

let subject: string | null = null;
let state: OnboardingState = INITIAL_ONBOARDING;
const subscribers = new Set<() => void>();
let wired = false;

function notify(): void {
  for (const s of [...subscribers]) s();
}

export function dispatchOnboarding(action: OnboardingAction): void {
  const next = reduceOnboarding(state, action);
  if (next === state) return;
  state = next;
  saveOnboarding(browserStorage(), subject, state);
  notify();
}

// bindSubject switches the store to `who` (sign-in; sign-out clears it).
// It runs during render, so it never notifies: every consumer renders with
// the same subject and reads the fresh snapshot.
function bindSubject(who: string | null): void {
  if (who === subject) return;
  subject = who;
  state = loadOnboarding(browserStorage(), who);
}

function wire(): void {
  if (wired || typeof window === "undefined") return;
  wired = true;
  onboardingEvents.subscribe((event) => dispatchOnboarding({ type: "event", event, at: Date.now() }));
  // Another tab of the same member ticked something: follow it.
  window.addEventListener("storage", (e) => {
    if (!subject || e.key !== onboardingKey(subject)) return;
    state = loadOnboarding(browserStorage(), subject);
    notify();
  });
}

function subscribe(cb: () => void): () => void {
  subscribers.add(cb);
  return () => {
    subscribers.delete(cb);
  };
}

// useOnboarding binds the store to the signed-in member and returns their
// state. Mounted once at the shell (App) so events tick even before the
// checklist renders.
export function useOnboarding(who: string | null): OnboardingState {
  wire();
  bindSubject(who);
  return useSyncExternalStore(subscribe, () => state, () => INITIAL_ONBOARDING);
}

// autoStartTour: the welcome card is showing its start button to a member.
// The tour starts at its first step when the start conditions hold
// (shouldAutoStartTour), so a brand-new member is guided from the first
// screen, not only once a room is open.
export function autoStartTour(who: string | null, priorRoomCount: number | null): void {
  wire();
  bindSubject(who);
  if (shouldAutoStartTour(state, priorRoomCount)) dispatchOnboarding({ type: "start", start: { tour: "main", step: 0 } });
}
