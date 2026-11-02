// Forwarding (channels PRD §2.3): a forward names its source and carries
// none of its text; the reader opens the source through the source room's
// own read, which answers only a reader of its channel. Pure, so node tests
// can import it.
import { agentHandle } from "./roomAgents.ts";
import type { FeedMessage } from "./wire.gen.ts";

/** sourceText is the words of a forwarded message as its room answers it:
 *  a person's text, or an agent reply's messages joined. */
export function sourceText(m: Pick<FeedMessage, "kind" | "payload">): string {
  const p = (m.payload ?? {}) as Record<string, unknown>;
  if (m.kind === "chat_message") return typeof p.text === "string" ? p.text : "";
  if (m.kind === "agent_messages" && Array.isArray(p.messages)) {
    return p.messages
      .map((x) => {
        const inner = (x as { payload?: { text?: unknown } }).payload;
        return typeof inner?.text === "string" ? inner.text.trim() : "";
      })
      .filter(Boolean)
      .join("\n");
  }
  return "";
}

/** sourceAuthor is who wrote the source: a person's id, or an agent's
 *  "owner/provider". */
export function sourceAuthor(m: Pick<FeedMessage, "kind" | "payload">): string {
  const p = (m.payload ?? {}) as Record<string, unknown>;
  if (m.kind === "chat_message") return typeof p.userId === "string" ? p.userId : "";
  if (m.kind === "agent_messages" && Array.isArray(p.messages) && p.messages.length > 0) {
    const inner = (p.messages[0] as { payload?: { ownerUserId?: unknown; provider?: unknown } }).payload;
    if (typeof inner?.ownerUserId === "string" && typeof inner.provider === "string") return agentHandle({ ownerId: inner.ownerUserId, provider: inner.provider });
  }
  return "";
}

/** A forward's excerpt: its first lines, at most max characters. */
export function excerpt(text: string, max = 280): string {
  const t = text.trim();
  return t.length > max ? `${t.slice(0, max - 1)}…` : t;
}
