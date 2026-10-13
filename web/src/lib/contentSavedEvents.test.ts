import { contentSaves, isOwnSave, parseContentSaved, submittedMessage, type ContentSaved } from "./contentSavedEvents.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

const saved = parseContentSaved({ componentId: "cc_1", kind: "prompt", name: "kit", displayName: "Kit", version: "1.0.0", action: "saved", created: true, owner: "maya" });
assert(saved !== null && saved.kind === "prompt" && saved.created === true && saved.displayName === "Kit", `parsed: ${JSON.stringify(saved)}`);
assert(parseContentSaved({ componentId: "cc_1", kind: "core", name: "x", version: "1", action: "saved", owner: "maya" }) === null, "unknown kind refused");
assert(parseContentSaved({ componentId: "cc_1", kind: "expert", name: "x", version: "1", action: "deleted", owner: "maya" }) === null, "unknown action refused");
assert(parseContentSaved({ kind: "expert", version: "1", action: "saved", owner: "maya" }) === null, "no component refused");
assert(parseContentSaved(undefined) === null, "no payload");

// Only the member it was saved for toasts it.
assert(isOwnSave(saved as ContentSaved, "maya") && !isOwnSave(saved as ContentSaved, "jack") && !isOwnSave(saved as ContentSaved, null), "own save");
assert(
  submittedMessage({ kind: "skill", name: "review-kit", version: "1.2.0", action: "submitted" }) === "Asked your organization's admins to publish the skill pack review-kit v1.2.0",
  "submitted message",
);
assert(
  submittedMessage({ kind: "app", name: "lunch-poll", displayName: "Lunch poll", version: "1.0.0", action: "published" }) === "Published the app Lunch poll v1.0.0 to your organization",
  "published message",
);
assert(parseContentSaved({ componentId: "cc_1", kind: "expert", name: "x", version: "1", action: "published", owner: "maya" })?.action === "published", "a publication without approval");

// The emitter reaches subscribers until they leave.
const seen: string[] = [];
const off = contentSaves.subscribe((room, s) => seen.push(`${room}:${s.componentId}`));
contentSaves.emit("room-1", saved as ContentSaved);
off();
contentSaves.emit("room-2", saved as ContentSaved);
assert(seen.join(",") === "room-1:cc_1", `seen ${seen.join(",")}`);
