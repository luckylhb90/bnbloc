// The room composer is fresh per channel (its draft, picked agent and attachments
// belong to one channel), so RoomView keys it by the channel. The channel is null
// until the room's channel list arrives and its default is selected: that first
// null -> channel step is the same conversation, not another one, and must not
// replace the composer (the member may already be typing). Every later change is a
// different channel and does.

export interface ComposerKeyState {
  readonly channelId: string | null;
  readonly key: string;
  /** A channel was selected before: the first one is not a change. */
  readonly resolved: boolean;
}

export const FIRST_COMPOSER_KEY: ComposerKeyState = { channelId: null, key: "", resolved: false };

/** nextComposerKey is the composer's key after the active channel became channelId. */
export function nextComposerKey(prev: ComposerKeyState, channelId: string | null): ComposerKeyState {
  if (channelId === prev.channelId) return prev;
  // The default channel resolving for the first time keeps the composer as it is.
  if (!prev.resolved && channelId !== null) return { channelId, key: prev.key, resolved: true };
  return { channelId, key: channelId ?? "", resolved: prev.resolved };
}
