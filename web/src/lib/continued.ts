// payload.continuedFrom is on a chat_message the platform posted as the member
// because their decision on a component's card lets an agent's work go on
// (docs/prd/2026-10-28-held-change-continues.md). It is read here, once, so the
// feed badges the message as automatic and nothing else guesses from its text.

/** continuedMarker is the service whose card was decided, or undefined. */
export function continuedMarker(payload: unknown): string | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const c = (payload as { continuedFrom?: unknown }).continuedFrom;
  if (!c || typeof c !== "object") return undefined;
  const name = (c as { component?: unknown }).component;
  return typeof name === "string" && name ? name : undefined;
}
