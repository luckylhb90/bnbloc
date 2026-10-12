import { compactNumber, formatUsd } from "./format.ts";

// Monthly usage budgets (R12): org admins set token/cost budgets per member
// and for the organization (GET/PUT /v1/tenant/budgets); a member reads their
// own (GET /v1/my/budget). The month is the UTC calendar month; burn is the
// usage the member's agents reported on their replies (a borrowed run counts
// against the agent's owner).

export interface BudgetLimits {
  tokens?: number;
  cost?: number;
  warnAtPercent: number;
  hardStop: boolean;
}

export interface UsageBurn {
  tokens: number;
  cost: number;
}

export interface BudgetView extends BudgetLimits {
  userId?: string;
  burn: UsageBurn;
  percent: number;
  reached: boolean;
  updatedBy?: string;
  updatedAtMs?: number;
}

export interface BudgetPeriod {
  period: string;
  periodStartMs: number;
  periodEndMs: number;
}

export interface MemberBurn extends UsageBurn {
  userId: string;
}

export interface TenantBudgets extends BudgetPeriod {
  tenant: BudgetView | null;
  members: BudgetView[];
  usage: MemberBurn[];
}

export interface MyBudget extends BudgetPeriod {
  budget: BudgetView | null;
  burn: UsageBurn;
  blocked: boolean;
  blockedBy?: "member" | "tenant";
}

export interface TenantBudgetsUpdate {
  tenant: BudgetLimits | null;
  members: (BudgetLimits & { userId: string })[];
}

// BudgetDraft is one budget row being edited (form strings).
export interface BudgetDraft {
  userId: string;
  tokens: string;
  cost: string;
  warnAtPercent: string;
  hardStop: boolean;
}

export const DEFAULT_WARN_AT_PERCENT = 80;

export function emptyDraft(userId = ""): BudgetDraft {
  return { userId, tokens: "", cost: "", warnAtPercent: String(DEFAULT_WARN_AT_PERCENT), hardStop: false };
}

export function draftFromView(v: BudgetView): BudgetDraft {
  return {
    userId: v.userId ?? "",
    tokens: v.tokens === undefined ? "" : String(v.tokens),
    cost: v.cost === undefined ? "" : String(v.cost),
    warnAtPercent: String(v.warnAtPercent || DEFAULT_WARN_AT_PERCENT),
    hardStop: v.hardStop,
  };
}

// limitsFromDraft validates one draft the way the server does.
export function limitsFromDraft(d: BudgetDraft): { limits?: BudgetLimits; error?: string } {
  const tokensText = d.tokens.trim();
  const costText = d.cost.trim();
  if (!tokensText && !costText) return { error: "set a token or cost limit" };
  const limits: BudgetLimits = { warnAtPercent: DEFAULT_WARN_AT_PERCENT, hardStop: d.hardStop };
  if (tokensText) {
    const tokens = Number(tokensText);
    if (!Number.isInteger(tokens) || tokens < 1) return { error: "tokens must be a whole number of at least 1" };
    limits.tokens = tokens;
  }
  if (costText) {
    const cost = Number(costText);
    if (!Number.isFinite(cost) || cost <= 0) return { error: "cost must be greater than 0" };
    limits.cost = cost;
  }
  const warnText = d.warnAtPercent.trim();
  if (warnText) {
    const warn = Number(warnText);
    if (!Number.isInteger(warn) || warn < 1 || warn > 99) return { error: "warn at must be between 1 and 99 percent" };
    limits.warnAtPercent = warn;
  }
  return { limits };
}

// buildBudgetsUpdate turns the edited rows into the PUT body, naming the row
// of the first problem.
export function buildBudgetsUpdate(
  tenant: BudgetDraft | null,
  members: readonly BudgetDraft[],
): { update?: TenantBudgetsUpdate; error?: string } {
  const update: TenantBudgetsUpdate = { tenant: null, members: [] };
  if (tenant) {
    const r = limitsFromDraft(tenant);
    if (r.error) return { error: `Organization budget: ${r.error}` };
    update.tenant = r.limits ?? null;
  }
  const seen = new Set<string>();
  for (const m of members) {
    const userId = m.userId.trim();
    if (!userId) return { error: "Every member budget needs a member" };
    if (seen.has(userId)) return { error: `${userId} has two budgets` };
    seen.add(userId);
    const r = limitsFromDraft(m);
    if (r.error) return { error: `${userId}: ${r.error}` };
    update.members.push({ userId, ...(r.limits as BudgetLimits) });
  }
  return { update };
}

export type BudgetTone = "ok" | "warn" | "over";

// budgetTone colours a burn bar: over once reached, warn past the threshold.
export function budgetTone(v: Pick<BudgetView, "percent" | "reached" | "warnAtPercent">): BudgetTone {
  if (v.reached) return "over";
  return v.percent >= v.warnAtPercent ? "warn" : "ok";
}

// describeBurn renders "1.2k of 10k tokens · $1.50 of $10.00" for the limits
// a budget sets.
export function describeBurn(v: BudgetLimits & { burn: UsageBurn }): string {
  const parts: string[] = [];
  if (v.tokens !== undefined) parts.push(`${compactNumber(v.burn.tokens)} of ${compactNumber(v.tokens)} tokens`);
  if (v.cost !== undefined) parts.push(`${formatUsd(v.burn.cost)} of ${formatUsd(v.cost)}`);
  return parts.join(" · ");
}

// describeLimits renders a budget's limits ("10k tokens · $25.00 / month").
export function describeLimits(v: BudgetLimits): string {
  const parts: string[] = [];
  if (v.tokens !== undefined) parts.push(`${compactNumber(v.tokens)} tokens`);
  if (v.cost !== undefined) parts.push(formatUsd(v.cost));
  return parts.length ? `${parts.join(" · ")} / month` : "";
}

// periodLabel renders "October 2026 (UTC)" for a "YYYY-MM" period.
export function periodLabel(period: string): string {
  const m = /^(\d{4})-(\d{2})$/.exec(period);
  if (!m) return period;
  const d = new Date(Date.UTC(Number(m[1]), Number(m[2]) - 1, 1));
  return `${d.toLocaleString("en-US", { month: "long", year: "numeric", timeZone: "UTC" })} (UTC)`;
}
