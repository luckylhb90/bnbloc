// Attachment ticket URLs. An <img src> or <a href download> cannot carry
// the session bearer, so each attachment link carries a short-lived ticket
// for that one attachment instead (gateway attachment_tickets.go); the
// session token never appears in a URL (only the room event stream's
// EventSource does that, in sse.ts).
//
// The cache is keyed by the session as well as the attachment: a ticket
// acts as the member who minted it, so a sign-in as someone else never
// reuses one.

export interface AttachmentTicket {
  readonly url: string;
  readonly expiresAtMs: number;
}

// A ticket this close to expiry is renewed before it is handed out.
export const ATTACHMENT_TICKET_SLACK_MS = 60_000;

export interface AttachmentTicketCache {
  // A still-valid ticket URL without minting, or null.
  cached(session: string, roomId: string, attachmentId: string): string | null;
  // A valid ticket URL, minting (once per key at a time) when needed.
  url(session: string, roomId: string, attachmentId: string): Promise<string>;
}

export function createAttachmentTicketCache(
  mint: (roomId: string, attachmentId: string) => Promise<AttachmentTicket>,
  now: () => number = Date.now,
): AttachmentTicketCache {
  const tickets = new Map<string, AttachmentTicket>();
  const inflight = new Map<string, Promise<string>>();
  const keyOf = (session: string, roomId: string, attachmentId: string) => `${session}\n${roomId}\n${attachmentId}`;
  const fresh = (t: AttachmentTicket | undefined) => !!t && t.expiresAtMs - ATTACHMENT_TICKET_SLACK_MS > now();
  return {
    cached(session, roomId, attachmentId) {
      const t = tickets.get(keyOf(session, roomId, attachmentId));
      return fresh(t) ? t!.url : null;
    },
    url(session, roomId, attachmentId) {
      const key = keyOf(session, roomId, attachmentId);
      const t = tickets.get(key);
      if (fresh(t)) return Promise.resolve(t!.url);
      let p = inflight.get(key);
      if (!p) {
        p = mint(roomId, attachmentId)
          .then((minted) => {
            tickets.set(key, minted);
            return minted.url;
          })
          .finally(() => inflight.delete(key));
        inflight.set(key, p);
      }
      return p;
    },
  };
}
