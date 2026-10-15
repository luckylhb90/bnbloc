import { createContext, useContext } from "react";

// Automate an app (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md
// §4): the app's card opens the room's schedule editor with an AI task
// about that app. The room provides it (RoomView: its schedules dialog);
// a surface without the room (a popped-out preview, a share page) has none
// and shows no "Automate…".

export interface AutomateApp {
  // Opens a new AI task schedule naming the app.
  readonly open: (app: { readonly artifactId: string; readonly title: string }) => void;
  // Opens the room's group chats (connect one for the app to notify).
  readonly groups: () => void;
}

const AutomateContext = createContext<AutomateApp | null>(null);
export const AutomateProvider = AutomateContext.Provider;

export function useAutomateApp(): AutomateApp | null {
  return useContext(AutomateContext);
}

// automateTaskSeed is the task an app's "Automate…" starts from: the app
// named by its title and id, for the member to finish in their words.
export function automateTaskSeed(app: { readonly artifactId: string; readonly title: string }): string {
  return `For the app "${app.title}" (${app.artifactId}): `;
}
