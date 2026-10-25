import { useState } from "react";
import { CopyIcon, LinkIcon } from "@phosphor-icons/react";
import { errorText } from "@/lib/errors";
import { shareProposalRequest, shareProposalWords, type ShareProposal } from "@/lib/shareProposal";
import { sharesApi } from "@/lib/sharesApi";
import { Button } from "@/ui/Button";
import { useCopy } from "@/ui/useCopy";
import { ShareDialog } from "@/views/workbench/ShareDialog";

// ShareProposalCard is an agent's offer of a link to one of the room's
// deliverables (room_setup share): only the person it was for sees it
// (feedItemFromEvent), and nothing is shared until they click Create link,
// which calls the share route as them with the dialog's defaults (the
// organization's signed-in members, a week). The link then shows here with a
// Copy button, and "More options" opens the Share dialog to widen who may
// open it or change its lifetime.
export function ShareProposalCard({ proposal }: { readonly proposal: ShareProposal }) {
  const [state, setState] = useState<{ busy?: boolean; url?: string; error?: string; dismissed?: boolean }>({});
  const [dialog, setDialog] = useState(false);
  const copy = useCopy();
  if (state.dismissed) return null;
  const create = async () => {
    setState({ busy: true });
    try {
      const res = await sharesApi.create(proposal.roomId, proposal.artifactId, shareProposalRequest());
      setState({ url: res.url });
      void copy(res.url, "link");
    } catch (e) {
      setState({ error: errorText(e, "Could not create the link") });
    }
  };
  return (
    <div data-share-proposal className="ml-10 mt-1 flex max-w-xl flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2 text-sm">
      <div className="flex items-start gap-2">
        <LinkIcon size={16} className="mt-0.5 shrink-0 text-[var(--color-accent)]" aria-hidden="true" />
        <p className="min-w-0 flex-1">{shareProposalWords(proposal)}</p>
      </div>
      {state.url ? (
        <div className="flex flex-col gap-1.5 pl-6">
          <input
            readOnly
            value={state.url}
            onFocus={(e) => e.currentTarget.select()}
            aria-label="The link"
            className="h-8 w-full rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 text-xs"
          />
          <div className="flex items-center gap-2">
            <Button size="sm" variant="primary" onClick={() => void copy(state.url ?? "", "link")}>
              <CopyIcon size={14} aria-hidden="true" /> Copy
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setDialog(true)}>
              More options…
            </Button>
          </div>
        </div>
      ) : (
        <div className="flex items-center gap-2 pl-6">
          <Button size="sm" variant="primary" onClick={() => void create()} disabled={state.busy}>
            {state.busy ? "Creating…" : "Create link"}
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setDialog(true)} disabled={state.busy}>
            More options…
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
      <ShareDialog
        open={dialog}
        onClose={() => setDialog(false)}
        roomId={proposal.roomId}
        artifactId={proposal.artifactId}
        fileName={proposal.title}
        version={0}
        kind={proposal.kind === "app" ? "app" : proposal.kind === "site" ? "site" : "file"}
      />
    </div>
  );
}
