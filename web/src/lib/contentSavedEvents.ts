// The room tools saved (or asked to publish) a content component for a
// member: the transient content_saved room event (internal/wire/
// content_events.go), published by the gateway in the job's room. The room
// view forwards it here and the workshop toasts the member's own saves, so
// nothing re-lists the Library after every agent reply.

import { contentKindWord, type ContentKind } from "./contentComponents.ts";

export type { ContentSaved } from "./wire.gen.ts";
import type { ContentSaved } from "./wire.gen.ts";

// The room event kind.
export const CONTENT_SAVED = "content_saved";

const KINDS: readonly ContentKind[] = ["expert", "skill", "prompt"];

// parseContentSaved reads the event's payload, or null when it is not one.
export function parseContentSaved(payload: Record<string, unknown> | undefined): ContentSaved | null {
  if (!payload) return null;
  const str = (k: string) => (typeof payload[k] === "string" ? (payload[k] as string) : "");
  const kind = str("kind") as ContentKind;
  const action = str("action");
  if (!str("componentId") || !str("version") || !str("owner") || !KINDS.includes(kind) || (action !== "saved" && action !== "submitted")) return null;
  return {
    componentId: str("componentId"),
    kind,
    name: str("name"),
    ...(str("displayName") ? { displayName: str("displayName") } : {}),
    version: str("version"),
    action,
    created: payload.created === true,
    owner: str("owner"),
  };
}

// isOwnSave: the event concerns the signed-in member (only they toast it).
export function isOwnSave(s: ContentSaved, subject: string | null): boolean {
  return !!subject && s.owner === subject;
}

// submittedMessage is the toast of a publish request the room tools opened.
export function submittedMessage(s: Pick<ContentSaved, "kind" | "name" | "displayName" | "version">): string {
  const word = contentKindWord(s.kind);
  return `Asked your organization's admins to publish the ${word} ${s.displayName || s.name} v${s.version}`;
}

type Listener = (roomId: string, s: ContentSaved) => void;

const listeners = new Set<Listener>();

export const contentSaves = {
  emit(roomId: string, s: ContentSaved): void {
    for (const l of [...listeners]) l(roomId, s);
  },
  // subscribe returns the unsubscribe function.
  subscribe(l: Listener): () => void {
    listeners.add(l);
    return () => {
      listeners.delete(l);
    };
  },
};
