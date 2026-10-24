import { useState } from "react";
import { PlugsConnectedIcon } from "@phosphor-icons/react";
import { componentPagesApi, openComponentPage } from "@/lib/componentPagesApi";
import { openExternal } from "@/lib/desktop";
import type { ComponentCard, ComponentCardAction } from "@/lib/componentCard";
import { errorText } from "@/lib/errors";
import { shareComposerRef } from "@/lib/team";
import { Button } from "@/ui/Button";

// ComponentCardView is a service component's card for the person a turn ran
// for (docs/architecture/service-components.md): only they see it
// (feedItemFromEvent). Its text is plain. "Open" opens one of the
// service's pages as them, for this room (a connection to make, a change
// to allow, who their agents act as here); a "call" acts in place (allow a
// change, start a sign-in) and the card shows the service's answer; "Ask
// again" puts the card in the composer, so the member asks the agent to
// retry in the same conversation.
export function ComponentCardView({ roomId, card }: { readonly roomId: string; readonly card: ComponentCard }) {
  const [state, setState] = useState<{ busy?: boolean; error?: string; dismissed?: boolean; asked?: boolean; reply?: string; done?: boolean }>({});
  if (state.dismissed) return null;
  const act = async (a: ComponentCardAction) => {
    if (a.kind === "ask-again") {
      shareComposerRef(roomId, { id: 0, author: card.component, text: card.title });
      setState({ asked: true });
      return;
    }
    setState((s) => ({ ...s, busy: true, error: undefined }));
    try {
      if (a.kind === "call") {
        const r = await componentPagesApi.callCard(card.component, a.page ?? "", a.query, roomId);
        if (r.open) await openExternal(r.open);
        setState({ reply: r.text, done: r.done });
        return;
      }
      await openComponentPage(card.component, a.page ?? "", a.query, roomId);
      setState({});
    } catch (e) {
      setState({ error: errorText(e, "Could not open the page") });
    }
  };
  return (
    <div data-component-card className="ml-10 mt-1 flex max-w-xl flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2 text-sm">
      <div className="flex items-start gap-2">
        <PlugsConnectedIcon size={16} className="mt-0.5 shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <p className="font-medium">{card.title}</p>
          {card.text && <p className="whitespace-pre-wrap text-[var(--color-fg-muted)]">{card.text}</p>}
          <p className="mt-1 text-xs text-[var(--color-fg-subtle)]">From {card.component}. Only you see this.</p>
        </div>
      </div>
      {state.reply && (
        <p role="status" className="whitespace-pre-wrap pl-6 text-[var(--color-fg)]">
          {state.reply}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2 pl-6">
        {!state.done && (card.actions ?? []).map((a, i) => (
          <Button key={`${a.kind}-${i}`} size="sm" variant={i === 0 ? "primary" : "secondary"} onClick={() => void act(a)} disabled={state.busy}>
            {a.label}
          </Button>
        ))}
        <Button size="sm" variant="ghost" onClick={() => setState({ dismissed: true })} disabled={state.busy}>
          {state.done ? "Close" : "Not now"}
        </Button>
        {state.asked && (
          <span role="status" className="text-xs text-[var(--color-fg-muted)]">
            Added to the composer: send it to ask the agent again.
          </span>
        )}
        {state.error && (
          <span role="alert" className="text-xs text-[var(--color-danger)]">
            {state.error}
          </span>
        )}
      </div>
    </div>
  );
}
