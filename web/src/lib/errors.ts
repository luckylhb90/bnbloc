// Failed requests and how they read to a person: the one owner (web M2-M4).
// A request that fails is an ApiError (status + the server's message); a
// view renders any thrown value with errorText, and words a status with
// statusText. No view writes `e instanceof Error ? e.message : …` or
// String(e) (web boundary rule 4): those leaked "Failed to fetch", a
// SyntaxError or a proxy's HTML page into toasts.

// ApiError is a failed request: the HTTP status and the server's own
// message (lib/session.ts errorDetail reads it from the body).
export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// statusText is how a status reads when there is nothing better to say: the
// server sent no usable message, or a view words the status itself.
export function statusText(status: number): string {
  switch (status) {
    case 401:
      return "Your session has ended. Sign in again.";
    case 403:
      return "You don't have access to that.";
    case 404:
      return "It no longer exists.";
    case 409:
      return "It was changed in the meantime. Reload and try again.";
    case 413:
      return "That is too large.";
    case 429:
      return "Too many requests. Wait a moment and try again.";
    case 502:
    case 503:
    case 504:
      return `The server is temporarily unavailable (${status}). Try again in a moment.`;
  }
  return `Request failed (${status})`;
}

// The network failed before any answer (fetch rejects with a TypeError).
export const UNREACHABLE = "Can't reach the server. Check your connection and try again.";

// errorReason is why something failed, for a person, or "" when the thrown
// value says nothing usable.
export function errorReason(e: unknown): string {
  if (e instanceof ApiError) return e.message || statusText(e.status);
  if (e instanceof TypeError) return UNREACHABLE;
  if (e instanceof SyntaxError) return "The server sent an answer that could not be read.";
  if (e instanceof Error) return e.message;
  return typeof e === "string" ? e : "";
}

// errorText renders a failure: what failed (fallback) and why, when known
// ("Could not join room: the room is private").
export function errorText(e: unknown, fallback: string): string {
  const reason = errorReason(e);
  if (!reason) return fallback;
  return fallback ? `${fallback}: ${reason}` : reason;
}
