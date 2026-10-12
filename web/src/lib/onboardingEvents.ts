// The onboarding event bus (PRD 2026-10-10 display names + onboarding §3):
// the real product events the guided tour and the getting-started checklist
// react to. Producers are the places that already see the event — the room
// feed (own echo, agent reply, from the stream or history), room activity
// (artifact_updated, the room's existing deliverables), the send action, the
// preview workbench, and the share / invite / add-member API calls. The bus
// is in-tab and synchronous; consumers (useOnboarding) map events onto the
// signed-in member's persisted state. Pure: node-tested.
import type { RoomEvent } from "./roomEvents.ts";

// Where an event was seen. "action" is the member's own request succeeding
// (the send POST answered); "stream" is the live room stream (it may replay
// recent events on connect); "history" is a persisted page or list read. The
// checklist ticks from any source; the tour advances only on fresh signals.
export type OnboardingSource = "action" | "stream" | "history";

export type OnboardingEvent =
  | { readonly kind: "message_sent"; readonly roomId: string; readonly source: OnboardingSource; readonly seq?: number; readonly toAgent: boolean }
  | { readonly kind: "agent_reply"; readonly roomId: string; readonly source: OnboardingSource; readonly seq?: number }
  | { readonly kind: "deliverable"; readonly roomId: string; readonly source: OnboardingSource }
  | { readonly kind: "artifact_opened"; readonly roomId: string }
  | { readonly kind: "share_created"; readonly roomId: string }
  | { readonly kind: "invite_created"; readonly roomId: string }
  | { readonly kind: "room_opened"; readonly roomId: string };

type Listener = (e: OnboardingEvent) => void;
const listeners = new Set<Listener>();

export const onboardingEvents = {
  emit(e: OnboardingEvent | null): void {
    if (!e) return;
    for (const l of [...listeners]) {
      try {
        l(e);
      } catch {
        // a consumer's failure never breaks the producer's flow
      }
    }
  },
  subscribe(l: Listener): () => void {
    listeners.add(l);
    return () => {
      listeners.delete(l);
    };
  },
};

// onboardingEventFromRoomEvent maps one normalized room event (live or from
// history) onto the onboarding event it means for `subject`, or null: the
// member's own chat message is a send, an assistant message is an agent
// reply, an artifact_updated is a deliverable. Self matching is
// case-insensitive (the feed's isSelf rule).
export function onboardingEventFromRoomEvent(
  e: RoomEvent,
  subject: string | null,
  roomId: string,
  source: Exclude<OnboardingSource, "action">,
): OnboardingEvent | null {
  if (e.kind === "artifact_updated") return { kind: "deliverable", roomId, source };
  if (!e.text) return null;
  if (e.kind === "agent_messages" && e.author === "agent") {
    return { kind: "agent_reply", roomId, source, seq: e.sequence };
  }
  if (e.kind === "chat_message" && subject && e.author && e.author.toLowerCase() === subject.toLowerCase()) {
    return { kind: "message_sent", roomId, source, seq: e.sequence, toAgent: e.toAgent !== false };
  }
  return null;
}

// tapOnboarding returns a pass-through for a promise chain that emits `e`
// once the request succeeded: `api.call().then(tapOnboarding(event))`.
export function tapOnboarding<T>(e: OnboardingEvent): (value: T) => T {
  return (value) => {
    onboardingEvents.emit(e);
    return value;
  };
}
