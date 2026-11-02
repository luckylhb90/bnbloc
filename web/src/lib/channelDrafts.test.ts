import { draftAction, restoreDraft } from "./channelDrafts.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq(restoreDraft("", "kept"), "kept", "an empty composer takes the stored draft");
eq(restoreDraft("  ", "kept"), "kept", "a blank composer takes the stored draft");
eq(restoreDraft("typed", "kept"), "typed", "what was typed wins");

eq(draftAction(false, "", ""), "none", "nothing before the draft was read");
eq(draftAction(false, "", "typed"), "none", "not even typing, before the read");
eq(draftAction(true, "same", "same"), "none", "unchanged");
eq(draftAction(true, "", "new"), "save", "new text is saved");
eq(draftAction(true, "old", "new"), "save", "changed text is saved");
eq(draftAction(true, "old", " "), "delete", "blank text deletes");
eq(draftAction(true, "", ""), "none", "nothing to delete");
console.log("channelDrafts tests passed");
