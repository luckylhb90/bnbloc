// A service component's card (docs/architecture/service-components.md): a
// notice row of kind NoticeComponentCard carrying payload.card, shown only
// to the person the turn ran for. Its text is plain; its actions open one of
// the service's pages, act in place (call), or ask the agent again. The
// helpers here are pure.
import { sameUser } from "./format.ts";
import type { ComponentCard, ComponentCardAction } from "./wire.gen.ts";

export type { ComponentCard, ComponentCardAction } from "./wire.gen.ts";

const str = (v: unknown): string => (typeof v === "string" ? v : "");

// componentCardOf reads a notice's payload.card, or undefined when it is not
// a whole one.
export function componentCardOf(v: unknown): ComponentCard | undefined {
  if (!v || typeof v !== "object") return undefined;
  const c = v as Record<string, unknown>;
  const actions: ComponentCardAction[] = [];
  if (Array.isArray(c.actions)) {
    for (const a of c.actions.slice(0, 3)) {
      if (!a || typeof a !== "object") continue;
      const r = a as Record<string, unknown>;
      const kind = str(r.kind);
      const label = str(r.label);
      if (!label || (kind !== "open" && kind !== "call" && kind !== "ask-again")) continue;
      if ((kind === "open" || kind === "call") && !/^[a-z0-9-]{1,32}$/.test(str(r.page))) continue;
      actions.push({ kind, label, page: str(r.page) || undefined, query: str(r.query) || undefined });
    }
  }
  const card: ComponentCard = { component: str(c.component), for: str(c.for), title: str(c.title), text: str(c.text) || undefined, actions };
  return card.component && card.for && card.title ? card : undefined;
}

// cardShownTo reports whether subject is shown the card: only the person
// the turn ran for.
export function cardShownTo(c: ComponentCard, subject: string | null): boolean {
  return sameUser(c.for, subject);
}
