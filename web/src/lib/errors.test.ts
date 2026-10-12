import { ApiError, UNREACHABLE, errorReason, errorText, statusText } from "./errors.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// The server's message says why; the fallback says what failed.
assert(errorText(new ApiError(403, "room is private"), "Could not join room") === "Could not join room: room is private", "server reason");
assert(errorText(new ApiError(503, ""), "Save failed") === `Save failed: ${statusText(503)}`, "an empty message reads as its status");
// What leaked before: a network failure, an unreadable body, a non-Error.
assert(errorText(new TypeError("Failed to fetch"), "Save failed") === `Save failed: ${UNREACHABLE}`, "network failure");
assert(!errorText(new SyntaxError("Unexpected token <"), "Save failed").includes("Unexpected token"), "a SyntaxError is never shown raw");
assert(errorText({ toString: () => "[object Object]" }, "Save failed") === "Save failed", "a non-Error says nothing");
assert(errorText(new ApiError(400, "name taken"), "") === "name taken", "no fallback: the reason alone");
assert(errorReason(undefined) === "", "nothing thrown");
// One wording per status.
assert(statusText(429).startsWith("Too many requests"), "429");
assert(statusText(418) === "Request failed (418)", "unknown status");
