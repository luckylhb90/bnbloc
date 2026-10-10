// roomIdProblem mirrors the server's room id rule (services/room): 1–64 bytes
// of UTF-8, no control characters, and none of / \ ? # % (ids are URL path
// segments). Returns a plain-English problem, or null when the id is valid.
const MAX_ROOM_ID_BYTES = 64;
const FORBIDDEN = /[/\\?#%]/;

export function roomIdProblem(raw: string): string | null {
  const id = raw.trim();
  if (!id) return "Room id is required";
  if (new TextEncoder().encode(id).length > MAX_ROOM_ID_BYTES) return "Room id must be at most 64 bytes";
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(id)) return "Room id cannot contain control characters";
  if (FORBIDDEN.test(id)) return "Room id cannot contain / \\ ? # or %";
  return null;
}
