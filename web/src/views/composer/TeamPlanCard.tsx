import { useEffect, useState } from "react";
import { CheckCircleIcon, UsersThreeIcon, WarningIcon } from "@phosphor-icons/react";
import { isTeam, type ExpertChoice } from "@/lib/contentComponents";
import { estimateText, planRequest, seatLine, teamConfirmKey, unstaffedRoles, type TeamPlan } from "@/lib/teams";
import { apiErrorText } from "@/lib/contentComponentsApi";
import { teamApi } from "@/lib/teamsApi";
import { Button } from "@/ui/Button";

// The lead agent a team runs on: the composer's resolved recipient.
export interface TeamLead {
  readonly ownerId: string;
  readonly core?: string;
  readonly provider: string;
}

export interface ComposerTeam {
  // The picked expert is a team.
  readonly active: boolean;
  readonly plan: TeamPlan | null;
  readonly loading: boolean;
  readonly error: string | null;
  // The member confirmed this team on this lead agent (sends are held
  // until they do).
  readonly confirmed: boolean;
  readonly confirm: () => void;
}

// useTeamPlan staffs a picked team on the chosen lead agent and estimates
// one request (POST /v1/rooms/{room}/team-plan); picking another team or
// lead asks for a new confirmation.
export function useTeamPlan(roomId: string, expert: ExpertChoice | null, lead: TeamLead | null): ComposerTeam {
  const team = expert && isTeam(expert.component) ? expert.component.team! : null;
  const version = expert ? expert.version || expert.component.latestVersion : "";
  const key = team && expert ? teamConfirmKey(expert.component.id, version, lead) : "";
  const [plan, setPlan] = useState<TeamPlan | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmedKey, setConfirmedKey] = useState("");

  useEffect(() => {
    setPlan(null);
    setError(null);
    if (!team || !lead || !key) return;
    let live = true;
    setLoading(true);
    teamApi
      .plan(roomId, planRequest(team, lead))
      .then((p) => live && setPlan(p))
      .catch((e) => live && setError(apiErrorText(e, "Could not staff the team")))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
    // key covers the team, its version and the lead.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [roomId, key]);

  return {
    active: !!team,
    plan,
    loading,
    error,
    confirmed: !team || (!!key && confirmedKey === key),
    confirm: () => setConfirmedKey(key),
  };
}

// TeamPlanCard shows a picked team before its first send: who takes each
// role, the pre-flight cost estimate, and the confirmation.
export function TeamPlanCard({ team, hasLead, onCancel }: { readonly team: ComposerTeam; readonly hasLead: boolean; readonly onCancel: () => void }) {
  if (!team.active) return null;
  const plan = team.plan;
  const unstaffed = plan ? unstaffedRoles(plan) : [];
  return (
    <div className="mx-3 mt-2 rounded-[var(--radius)] border border-[var(--color-accent)]/30 bg-[var(--color-accent-soft)] px-3 py-2 text-xs" data-team-plan>
      <div className="flex items-center gap-1.5 font-medium text-[var(--color-fg)]">
        <UsersThreeIcon size={14} className="text-[var(--color-accent)]" />
        {team.confirmed ? "Team ready" : "Run this team?"}
        {team.confirmed && <CheckCircleIcon size={14} weight="fill" className="text-[var(--color-accent)]" />}
      </div>
      {!hasLead ? (
        <p className="mt-1 text-[var(--color-fg-muted)]">Pick the agent that leads the team.</p>
      ) : team.loading ? (
        <p className="mt-1 text-[var(--color-fg-muted)]">Staffing the team…</p>
      ) : team.error ? (
        <p className="mt-1 text-[var(--color-danger)]">{team.error}</p>
      ) : plan ? (
        <>
          <ul className="mt-1 flex flex-col gap-0.5 text-[var(--color-fg-muted)]">
            {plan.roles.map((r) => (
              <li key={r.role} className={r.agent ? undefined : "text-[var(--color-warning)]"}>
                {seatLine(r)}
              </li>
            ))}
          </ul>
          <p className="mt-1 font-medium text-[var(--color-fg)]">{estimateText(plan.estimate)}</p>
          {unstaffed.length > 0 && (
            <p className="mt-1 flex items-center gap-1 text-[var(--color-warning)]">
              <WarningIcon size={12} /> The lead will refuse what {unstaffed.join(", ")} would cover.
            </p>
          )}
        </>
      ) : null}
      {!team.confirmed && (
        <div className="mt-2 flex gap-2">
          <Button size="sm" variant="primary" disabled={!plan} onClick={team.confirm}>
            Use this team
          </Button>
          <Button size="sm" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      )}
    </div>
  );
}
