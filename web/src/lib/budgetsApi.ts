import { req } from "./api.ts";
import type { MyBudget, TenantBudgets, TenantBudgetsUpdate } from "./budgets.ts";

export const budgetsApi = {
  // The organization's budgets with this month's burn (org admins).
  tenant: () => req<TenantBudgets>("/v1/tenant/budgets"),
  // Replaces the organization's whole budget set; answers the new view.
  saveTenant: (update: TenantBudgetsUpdate) =>
    req<TenantBudgets>("/v1/tenant/budgets", { method: "PUT", body: JSON.stringify(update) }),
  // The caller's own budget and burn this month.
  mine: () => req<MyBudget>("/v1/my/budget"),
};
