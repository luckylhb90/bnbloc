import { useState } from "react";
import { ShareNetworkIcon } from "@phosphor-icons/react";
import { contentSharingApi } from "@/lib/contentSharingApi";
import { errorText } from "@/lib/errors";
import { proposalLabel, type PublishProposal } from "@/lib/publishProposal";
import { Button } from "@/ui/Button";

// PublishProposalCard is an agent's proposal to share a component of the
// member's with their organization (components publish): only they see it
// (feedItemFromEvent), and nothing is shared until they click Publish,
// which calls the publish route as them. What the click did stays on the
// card.
export function PublishProposalCard({ proposal }: { readonly proposal: PublishProposal }) {
  const [state, setState] = useState<{ busy?: boolean; done?: string; error?: string; dismissed?: boolean }>({});
  if (state.dismissed) return null;
  const publish = async () => {
    setState({ busy: true });
    try {
      const reply = await contentSharingApi.publish(proposal.componentId, "org", proposal.note, proposal.version);
      setState({ done: reply.note || "Published to your organization." });
    } catch (e) {
      setState({ error: errorText(e, "Could not publish it") });
    }
  };
  return (
    <div data-publish-proposal className="ml-10 mt-1 flex max-w-xl flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2 text-sm">
      <div className="flex items-start gap-2">
        <ShareNetworkIcon size={16} className="mt-0.5 shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <p className="min-w-0 flex-1">
          Your agent proposes publishing the {proposalLabel(proposal)} to your organization. Only you see this; nothing is shared until you publish it.
        </p>
      </div>
      {proposal.note && <p className="pl-6 text-xs text-[var(--color-fg-muted)]">Note for the admins: {proposal.note}</p>}
      {state.done ? (
        <p role="status" className="pl-6 text-xs text-[var(--color-fg-muted)]">
          {state.done}
        </p>
      ) : (
        <div className="flex items-center gap-2 pl-6">
          <Button size="sm" variant="primary" onClick={() => void publish()} disabled={state.busy}>
            {state.busy ? "Publishing…" : "Publish"}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setState({ dismissed: true })} disabled={state.busy}>
            Not now
          </Button>
          {state.error && (
            <span role="alert" className="text-xs text-[var(--color-danger)]">
              {state.error}
            </span>
          )}
        </div>
      )}
    </div>
  );
}
