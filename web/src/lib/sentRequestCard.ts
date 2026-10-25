// The suggestion card of a sent request: which of the member's messages it is about, and
// when it goes away. A card belongs to one message (its client id), lives only in this
// session (never stored, never posted: a reload shows nothing), is replaced by the next
// request's card, and goes when the member dismisses it, acts on it or sends another
// message (docs/prd/2026-10-25-suggest-what-to-use.md). The views read it through
// useSyncExternalStore, so a card shows under its row without re-rendering the feed.
import type { UseSuggestion } from "./suggestUse.ts";

export interface SuggestionCard {
  /** The member's message the card is about (FeedItem.clientMsgId). */
  readonly clientMsgId: string;
  /** The text of that message: what asking again sends. */
  readonly request: string;
  readonly suggestion: UseSuggestion;
}

type Listener = () => void;

/** createCardStore holds at most one card per room. */
export function createCardStore() {
  const cards = new Map<string, SuggestionCard>();
  const listeners = new Set<Listener>();
  const changed = () => {
    for (const l of [...listeners]) l();
  };
  return {
    /** of is the room's card, or null; the same object until it changes. */
    of(roomId: string): SuggestionCard | null {
      return cards.get(roomId) ?? null;
    },
    /** show puts the card up for the room, replacing an older one. */
    show(roomId: string, card: SuggestionCard): void {
      cards.set(roomId, card);
      changed();
    },
    /** clear takes the room's card down; with a client id, only when it is that
     *  message's (an answer for an older message never removes a newer card). */
    clear(roomId: string, clientMsgId?: string): void {
      const cur = cards.get(roomId);
      if (!cur || (clientMsgId !== undefined && cur.clientMsgId !== clientMsgId)) return;
      cards.delete(roomId);
      changed();
    },
    subscribe(l: Listener): () => void {
      listeners.add(l);
      return () => {
        listeners.delete(l);
      };
    },
  };
}

/** The session's card store (views/room/useSentRequestSuggestion.ts). */
export const sentRequestCards = createCardStore();

/** cardForRow is the card to draw under the row with this client id, or null: a card
 *  is drawn only under its own message. */
export function cardForRow(card: SuggestionCard | null, clientMsgId: string | undefined): SuggestionCard | null {
  return card && clientMsgId && card.clientMsgId === clientMsgId ? card : null;
}

/** staleAnswer: whether an answer that arrived now is no longer wanted: the member has
 *  sent another message since (its client id is the room's latest), or left. */
export function staleAnswer(askedFor: string, latestClientMsgId: string | null, leftRoom: boolean): boolean {
  return leftRoom || (latestClientMsgId !== null && latestClientMsgId !== askedFor);
}
