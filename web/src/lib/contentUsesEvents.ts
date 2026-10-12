// A room's content uses changed (the transient content_uses_changed room
// event, published by the gateway on every pin, room expert and room pack
// change): the room view forwards it here, and the views that show a room's
// uses (the composer's experts, the room settings) refetch them, so a change
// shows without reopening the room.

type Listener = (roomId: string) => void;

const listeners = new Set<Listener>();

export const contentUsesChanged = {
  emit(roomId: string): void {
    for (const l of [...listeners]) l(roomId);
  },
  // subscribe returns the unsubscribe function.
  subscribe(l: Listener): () => void {
    listeners.add(l);
    return () => {
      listeners.delete(l);
    };
  },
};

// The room event kind (internal/wire/content_events.go).
export const CONTENT_USES_CHANGED = "content_uses_changed";
