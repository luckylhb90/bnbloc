// The rules of a composer's server-kept draft (channels PRD C6): what the
// composer shows when the stored draft arrives, and what a change of its
// text asks of the server. Pure, so node tests can import it.

export type DraftAction = "none" | "save" | "delete";

// restoreDraft is the composer's text once the stored draft arrives: what
// the member already typed (or was handed) wins; an empty composer takes
// the stored draft.
export function restoreDraft(current: string, stored: string): string {
  return current.trim() === "" ? stored : current;
}

// draftAction says what the server is asked for text. Nothing before the
// stored draft was read (an empty composer would wipe a draft another
// device kept), nothing when it is unchanged; blank text deletes.
export function draftAction(loaded: boolean, stored: string, text: string): DraftAction {
  if (!loaded || text === stored) return "none";
  if (text.trim() === "") return stored === "" ? "none" : "delete";
  return "save";
}
