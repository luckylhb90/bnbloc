// The platform agents usage dashboard (super admin,
// GET /v1/platform/platform-agents/usage; PRD 2026-10-10 v2 §2), built
// server-side from the borrow accountability rows of platform grants. Cost is
// what the bridge reports (0 for unpriced/trial models). Wire shapes and the
// pure formatting the Platform card renders.
import { compactNumber } from "./format.ts";
import { prettyModel } from "./modelLabel.ts";
import { formatCost } from "./teamDashboard.ts";

export interface PlatformUsageTotals {
  readonly runs: number;
  readonly members: number;
  readonly rooms: number;
  readonly tokens: number;
  readonly cost: number;
}

export interface PlatformUsageMember {
  readonly memberId: string;
  readonly runs: number;
  readonly tokens: number;
  readonly cost: number;
}

export interface PlatformUsageRoom {
  readonly roomId: string;
  readonly roomName: string;
  readonly tenantId: string;
  readonly runs: number;
  readonly tokens: number;
  readonly cost: number;
}

export interface PlatformUsageRun {
  readonly atMs: number;
  readonly memberId: string;
  readonly roomId: string;
  readonly roomName: string;
  readonly tenantId: string;
  // "<core>/<provider>"
  readonly agent: string;
  readonly hostUserId: string;
  // Empty: the run used the agent's default model.
  readonly model: string;
  readonly cost: number;
}

export interface PlatformAgentsUsage {
  readonly today: PlatformUsageTotals;
  readonly month: PlatformUsageTotals;
  // Top 5 of the month.
  readonly topMembers: readonly PlatformUsageMember[];
  readonly topRooms: readonly PlatformUsageRoom[];
  // The last 20 runs, newest first.
  readonly recent: readonly PlatformUsageRun[];
  // The server hit its row cap: the figures cover the newest rows only.
  readonly truncated: boolean;
}

const ZERO: PlatformUsageTotals = { runs: 0, members: 0, rooms: 0, tokens: 0, cost: 0 };

// normalizeUsage fills Go's null lists with empty ones.
export function normalizeUsage(raw: PlatformAgentsUsage): PlatformAgentsUsage {
  return {
    today: raw.today ?? ZERO,
    month: raw.month ?? ZERO,
    topMembers: raw.topMembers ?? [],
    topRooms: raw.topRooms ?? [],
    recent: raw.recent ?? [],
    truncated: !!raw.truncated,
  };
}

function plural(n: number, one: string, many: string): string {
  return `${compactNumber(n)} ${n === 1 ? one : many}`;
}

// usageTiles are the totals strip of one period: label + value.
export function usageTiles(t: PlatformUsageTotals): { readonly label: string; readonly value: string }[] {
  return [
    { label: "Runs", value: compactNumber(t.runs) },
    { label: "Members", value: compactNumber(t.members) },
    { label: "Rooms", value: compactNumber(t.rooms) },
    { label: "Tokens", value: compactNumber(t.tokens) },
    { label: "Cost", value: formatCost(t.cost) },
  ];
}

// usageQuery is the endpoint's query: the browser's UTC offset in minutes
// east, so "today" and "this month" are the admin's local calendar.
export function usageQuery(date: Date = new Date()): string {
  return `tzOffsetMin=${-date.getTimezoneOffset()}`;
}

// usageSummary is a one-line period summary ("3 runs · 2 members · $0.00").
export function usageSummary(t: PlatformUsageTotals): string {
  return `${plural(t.runs, "run", "runs")} · ${plural(t.members, "member", "members")} · ${formatCost(t.cost)}`;
}

// runModelLabel names the model of a run; an empty model ran on the agent's
// default. A trial-channel id stays verbatim (its prefix is meaningful).
export function runModelLabel(model: string | undefined): string {
  if (!model) return "default model";
  return isTrialModel(model) ? model : prettyModel(model);
}

// roomLabel prefers the room's name over its id.
export function roomLabel(r: { readonly roomId: string; readonly roomName?: string }): string {
  return r.roomName || r.roomId;
}

// TRIAL_MODEL_PREFIX marks a provider's trial channel (models offered free).
const TRIAL_MODEL_PREFIX = "x-";

export function isTrialModel(model: string): boolean {
  return model.startsWith(TRIAL_MODEL_PREFIX);
}

// modelOptionLabel is the default-model select's label: trial-channel models
// say they are free, so the admin can pick one at a glance.
export function modelOptionLabel(model: string): string {
  return isTrialModel(model) ? `${model} — trial channel · free` : model;
}

// splitTrialModels groups a catalog for the default-model select: the free
// trial-channel models first, then the rest, each in catalog order.
export function splitTrialModels(models: readonly string[]): { readonly trial: string[]; readonly other: string[] } {
  return { trial: models.filter(isTrialModel), other: models.filter((m) => !isTrialModel(m)) };
}
