import { useMemo, useState } from "react";
import { CheckIcon, XIcon } from "@phosphor-icons/react";
import { api } from "@/lib/api";
import { relativeTime } from "@/lib/format";
import { sortQuotaRequests, type QuotaRequest } from "@/lib/teamDashboard";
import { useVisiblePoll } from "@/lib/useVisiblePoll";
import { refreshQuotaRequests, useQuotaRequests } from "@/lib/queries";
import { Badge } from "@/ui/Card";
import { Button } from "@/ui/Button";
import { Input } from "@/ui/Input";
import { useToast } from "@/ui/Toast";
import { UserName } from "@/views/UserName";
import { displayNameOf } from "@/lib/useDisplayName";
import { errorText } from "@/lib/errors";

const POLL_MS = 30_000;

function statusTone(s: QuotaRequest["status"]): "warning" | "accent" | "neutral" {
  if (s === "pending") return "warning";
  if (s === "approved") return "accent";
  return "neutral";
}

// QuotaRequestsPanel lists borrow quota requests from one side: role "owner"
// shows requests addressed to the signed-in member (with approve/decline),
// role "requester" the member's own requests and their outcome. roomId narrows
// it to one room (the in-room authorization dialog); pendingOnly hides decided
// ones. onChanged fires after a decision so badges elsewhere refresh.
export function QuotaRequestsPanel({
  role,
  roomId,
  pendingOnly = false,
  emptyText,
  onChanged,
}: {
  readonly role: "owner" | "requester";
  readonly roomId?: string;
  readonly pendingOnly?: boolean;
  readonly emptyText: string;
  readonly onChanged?: () => void;
}) {
  const toast = useToast();
  const query = useQuotaRequests(role, { roomId, pendingOnly });
  const requests = useMemo(() => (query.data ? sortQuotaRequests(query.data) : null), [query.data]);
  const failed = !!query.error;
  const [busy, setBusy] = useState("");
  // requestId -> runs the owner will grant (defaults to the requested amount).
  const [grant, setGrant] = useState<Record<string, string>>({});

  useVisiblePoll(() => void query.refetch(), POLL_MS);

  const decide = async (req: QuotaRequest, approve: boolean) => {
    setBusy(req.requestId);
    try {
      if (approve) {
        const raw = (grant[req.requestId] ?? (req.requestedRuns ? String(req.requestedRuns) : "")).trim();
        const runs = Number(raw);
        // An access request authorizes the agent: an amount is optional
        // (none = no run limit for the authorization's lifetime).
        const access = req.kind === "access";
        if ((!access || raw) && (!raw || !Number.isInteger(runs) || runs < 1)) {
          toast.error("Enter how many runs to grant");
          return;
        }
        const res = await api.approveQuotaRequest(req.requestId, raw ? runs : undefined);
        toast.success(
          access
            ? `${displayNameOf(req.requester)} can now use your ${req.provider} in this room` + (raw ? ` (${runs} run${runs === 1 ? "" : "s"})` : "")
            : `Granted ${runs} more run${runs === 1 ? "" : "s"} to ${displayNameOf(req.requester)}` +
                (res.runQuota !== undefined ? ` (limit now ${res.runQuota})` : ""),
        );
      } else {
        await api.declineQuotaRequest(req.requestId);
        toast.info(`Declined ${displayNameOf(req.requester)}'s request`);
      }
      refreshQuotaRequests();
      onChanged?.();
    } catch (e) {
      toast.error(errorText(e, "Could not update the request"));
      refreshQuotaRequests();
    } finally {
      setBusy("");
    }
  };

  if (failed && requests === null) {
    return <p className="text-sm text-[var(--color-danger)]">Could not load quota requests.</p>;
  }
  if (requests === null) {
    return <p className="text-sm text-[var(--color-fg-subtle)]">Loading…</p>;
  }
  if (requests.length === 0) {
    return <p className="text-sm text-[var(--color-fg-subtle)]">{emptyText}</p>;
  }
  return (
    <ul className="flex flex-col gap-2">
      {requests.map((r) => {
        const pending = r.status === "pending";
        return (
          <li
            key={r.requestId}
            className="flex flex-col gap-2 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface-2)] px-3 py-2"
          >
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="font-medium text-[var(--color-fg)]">
                <UserName id={role === "owner" ? r.requester : r.owner} />
              </span>
              <span className="text-[var(--color-fg-muted)]">
                {r.kind === "access" ? (
                  <>
                    {role === "owner" ? "asks to use your" : "asked to use"}{" "}
                    <span className="text-[var(--color-fg)]">{r.provider}</span>
                  </>
                ) : (
                  <>
                    {role === "owner" ? "asks for" : "asked for"}{" "}
                    {r.requestedRuns ? `${r.requestedRuns} more run${r.requestedRuns === 1 ? "" : "s"}` : "more runs"} of{" "}
                    <span className="text-[var(--color-fg)]">{r.provider}</span>
                  </>
                )}
              </span>
              <Badge tone={statusTone(r.status)}>
                {r.status}
                {r.status === "approved" && r.grantedRuns ? ` · +${r.grantedRuns}` : ""}
              </Badge>
              <span className="ml-auto text-xs text-[var(--color-fg-subtle)] tabular">
                {relativeTime(r.createdAtMs)}
                {!roomId && ` · room ${r.roomId.slice(0, 8)}`}
              </span>
            </div>
            {r.note && <p className="text-sm text-[var(--color-fg-muted)] break-words">“{r.note}”</p>}
            {role === "owner" && pending && (
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  type="number"
                  min={1}
                  max={10000}
                  inputMode="numeric"
                  aria-label="Runs to grant"
                  placeholder={r.kind === "access" ? "Runs (optional)" : "Runs"}
                  className="h-8 w-24"
                  value={grant[r.requestId] ?? (r.requestedRuns ? String(r.requestedRuns) : "")}
                  onChange={(e) => setGrant((g) => ({ ...g, [r.requestId]: e.target.value }))}
                />
                <Button size="sm" variant="primary" disabled={busy === r.requestId} onClick={() => void decide(r, true)}>
                  <CheckIcon size={14} /> Approve
                </Button>
                <Button size="sm" variant="ghost" disabled={busy === r.requestId} onClick={() => void decide(r, false)}>
                  <XIcon size={14} /> Decline
                </Button>
              </div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

// usePendingQuotaCount polls how many requests await the signed-in owner
// (optionally in one room), for the badge on the borrow-authorization icon.
export function usePendingQuotaCount(roomId?: string): { count: number; refresh: () => void } {
  const query = useQuotaRequests("owner", { roomId, pendingOnly: true });
  useVisiblePoll(() => void query.refetch(), POLL_MS);
  return { count: query.data?.length ?? 0, refresh: refreshQuotaRequests };
}
