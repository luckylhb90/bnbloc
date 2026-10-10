import { useState } from "react";
import { UsersThreeIcon } from "@phosphor-icons/react";
import { useAuth } from "@/lib/auth";
import { cn } from "@/lib/cn";
import { TeamAgentsTab } from "@/views/team/TeamAgentsTab";
import { UsageBillingTab } from "@/views/team/UsageBillingTab";
import { TeamRunsTab } from "@/views/team/TeamRunsTab";
import { BudgetsTab } from "@/views/team/BudgetsTab";
import type { RunAgentFilter } from "@/views/runs/RunsBrowser";

type Tab = "agents" | "runs" | "usage" | "budgets";

const TABS: { id: Tab; label: string }[] = [
  { id: "agents", label: "Agents" },
  { id: "runs", label: "Runs" },
  { id: "usage", label: "Usage & billing" },
];

// Team is the team agent dashboard (F1) and borrow billing (F3). Org admins
// (and super-admins, in their acting tenant) see every member's agents and the
// organization-wide report; members see their own agents and their side of
// borrowing. The gateway enforces the same split (/v1/tenant/* is manage_org).
export function Team({ onOpenRoom }: { readonly onOpenRoom: (id: string) => void }) {
  const { role, tenant } = useAuth();
  const canSeeTeam = role === "org_admin" || role === "super_admin";
  const [tab, setTab] = useState<Tab>("agents");
  // The agent the Runs tab is narrowed to (an agent row's "Runs" link).
  const [runsAgent, setRunsAgent] = useState<RunAgentFilter | null>(null);

  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-6 p-4 md:p-8">
      <header className="flex items-center gap-3">
        <UsersThreeIcon size={26} weight="fill" className="text-[var(--color-accent)]" />
        <div className="min-w-0 flex-1">
          <h1 className="text-xl font-semibold tracking-tight">Team</h1>
          <p className="truncate text-sm text-[var(--color-fg-muted)]">
            {canSeeTeam ? `Agents and borrowing across ${tenant || "your organization"}` : "Your agents and borrowing"}
          </p>
        </div>
      </header>

      <nav className="flex gap-1 overflow-x-auto border-b border-[var(--color-border)]" role="tablist">
        {(canSeeTeam ? [...TABS, { id: "budgets" as const, label: "Budgets" }] : TABS).map((t) => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => setTab(t.id)}
            className={cn(
              "tactile -mb-px whitespace-nowrap border-b-2 px-3 py-2 text-sm",
              tab === t.id
                ? "border-[var(--color-accent)] text-[var(--color-fg)]"
                : "border-transparent text-[var(--color-fg-muted)] hover:text-[var(--color-fg)]",
            )}
          >
            {t.label}
          </button>
        ))}
      </nav>

      {tab === "agents" ? (
        <TeamAgentsTab
          canSeeTeam={canSeeTeam}
          onOpenRoom={onOpenRoom}
          onShowRuns={(a) => {
            setRunsAgent(a);
            setTab("runs");
          }}
        />
      ) : tab === "runs" ? (
        <TeamRunsTab canSeeTeam={canSeeTeam} agent={runsAgent} onClearAgent={() => setRunsAgent(null)} onOpenRoom={onOpenRoom} />
      ) : tab === "budgets" && canSeeTeam ? (
        <BudgetsTab />
      ) : (
        <UsageBillingTab canSeeTeam={canSeeTeam} onOpenRoom={onOpenRoom} />
      )}
    </div>
  );
}
