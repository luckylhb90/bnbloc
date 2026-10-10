package wire

// Usage budget DTOs (R12): GET/PUT /v1/tenant/budgets (org admins) and
// GET /v1/my/budget (any member). Budgets are monthly, over the UTC calendar
// month; burn is the token/cost usage the member's agents reported on their
// replies that month (borrowed runs count against the agent's owner).

// UsageBudgetLimits is one budget as an admin sets it. At least one of Tokens
// / Cost is set; an absent one is no limit on that dimension.
type UsageBudgetLimits struct {
	Tokens *int64   `json:"tokens,omitempty"`
	Cost   *float64 `json:"cost,omitempty"`
	// WarnAtPercent (1..99, default 80) sends the first warning; reaching
	// 100% sends the second.
	WarnAtPercent int `json:"warnAtPercent"`
	// HardStop refuses the member's own agent drives (tenant budget: every
	// drive in the organization) once the budget is reached.
	HardStop bool `json:"hardStop"`
}

// MemberBudgetLimits is a member budget in PUT /v1/tenant/budgets.
type MemberBudgetLimits struct {
	UserID string `json:"userId"`
	UsageBudgetLimits
}

// TenantBudgetsUpdate is the body of PUT /v1/tenant/budgets: the whole budget
// set (an absent tenant budget or member is removed).
type TenantBudgetsUpdate struct {
	Tenant  *UsageBudgetLimits   `json:"tenant"`
	Members []MemberBudgetLimits `json:"members"`
}

// UsageBurn is the usage measured against a budget in the period.
type UsageBurn struct {
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost"`
}

// BudgetView is one budget with its burn: Percent is the higher of the
// token and cost burn percentages (floored); Reached when either is at or
// over its limit.
type BudgetView struct {
	UserID string `json:"userId,omitempty"`
	UsageBudgetLimits
	Burn        UsageBurn `json:"burn"`
	Percent     int       `json:"percent"`
	Reached     bool      `json:"reached"`
	UpdatedBy   string    `json:"updatedBy,omitempty"`
	UpdatedAtMs int64     `json:"updatedAtMs,omitempty"`
}

// MemberBurn is one owner's burn in the period (budgeted or not).
type MemberBurn struct {
	UserID string `json:"userId"`
	UsageBurn
}

// BudgetPeriod is the month a budget reply covers.
type BudgetPeriod struct {
	Period        string `json:"period"` // "YYYY-MM" (UTC)
	PeriodStartMs int64  `json:"periodStartMs"`
	PeriodEndMs   int64  `json:"periodEndMs"`
}

// TenantBudgetsReply is GET (and the answer to PUT) /v1/tenant/budgets.
type TenantBudgetsReply struct {
	BudgetPeriod
	Tenant  *BudgetView  `json:"tenant"`
	Members []BudgetView `json:"members"`
	// Usage is every owner's burn this period, budgeted or not.
	Usage []MemberBurn `json:"usage"`
}

// MyBudgetReply is GET /v1/my/budget: the caller's own budget (nil when none)
// and burn, and whether their agents are stopped by a hard stop.
type MyBudgetReply struct {
	BudgetPeriod
	Budget *BudgetView `json:"budget"`
	Burn   UsageBurn   `json:"burn"`
	// Blocked is true when a hard-stop budget is reached; BlockedBy names it
	// ("member" = the caller's own, "tenant" = the organization's).
	Blocked   bool   `json:"blocked"`
	BlockedBy string `json:"blockedBy,omitempty"`
}
