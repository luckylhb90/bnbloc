package wire

// Team agent dashboard (F1) and borrow billing (F3) DTOs. The JSON tags are the
// public contract the web client reads. Optional fields are omitted when no
// data source backs them — a missing cost means "not reported", never zero.

// Team dashboard ranges.
//
//wire:union TeamRange
const (
	TeamRangeToday = "today"
	TeamRange7d    = "7d"
)

// TeamJob is one agent job as the dashboard shows it (no prompt text).
type TeamJob struct {
	JobID         string `json:"jobId"`
	RoomID        string `json:"roomId"`
	RoomName      string `json:"roomName,omitempty"`
	ChannelID     string `json:"channelId,omitempty"`
	Requester     string `json:"requester"`
	Status        string `json:"status" tstype:"AgentJobStatus"`
	FailureReason string `json:"failureReason,omitempty" tstype:"AgentJobFailureReason"`
	CreatedAtMs   int64  `json:"createdAtMs"`
	StartedAtMs   int64  `json:"startedAtMs,omitempty"`
	FinishedAtMs  int64  `json:"finishedAtMs,omitempty"`
}

// TeamAgentUsage is the reported reply metrics summed over the range. CostUsd
// is present only when at least one reply reported a cost. Shared marks usage
// that could not be split between two of the owner's cores exposing the same
// provider (replies carry the provider, not the core): each such row shows
// the provider's combined total.
type TeamAgentUsage struct {
	ReportedTurns int      `json:"reportedTurns"`
	InputTokens   int64    `json:"inputTokens"`
	OutputTokens  int64    `json:"outputTokens"`
	CostUsd       *float64 `json:"costUsd,omitempty"`
	Shared        bool     `json:"shared,omitempty"`
}

// TeamAgent is one member's agent on the dashboard. Turns counts jobs in the
// range that ended replied; FailedTurns those that ended failed/timed out;
// BorrowedRuns the replied jobs requested by someone other than the owner.
type TeamAgent struct {
	Owner        string          `json:"owner"`
	Core         string          `json:"core"`
	Provider     string          `json:"provider"`
	TargetID     string          `json:"targetId,omitempty"`
	Online       bool            `json:"online"`
	LastSeenAtMs int64           `json:"lastSeenAtMs,omitempty"`
	CurrentJob   *TeamJob        `json:"currentJob,omitempty"`
	LastJob      *TeamJob        `json:"lastJob,omitempty"`
	Turns        int             `json:"turns"`
	FailedTurns  int             `json:"failedTurns"`
	BorrowedRuns int             `json:"borrowedRuns"`
	Usage        *TeamAgentUsage `json:"usage,omitempty"`
}

// TeamAgentsReply is GET /v1/team/agents (scope "self") and
// GET /v1/tenant/team/agents (scope "tenant").
type TeamAgentsReply struct {
	Scope   string      `json:"scope"`
	Range   string      `json:"range" tstype:"TeamRange"`
	SinceMs int64       `json:"sinceMs"`
	Agents  []TeamAgent `json:"agents"`
}

// Borrow usage report grouping values.
//
//wire:union UsageGroupBy
const (
	UsageGroupDay   = "day"
	UsageGroupMonth = "month"
)

// Borrow usage report perspectives.
//
//wire:union UsageAs
const (
	UsageAsOwner    = "owner"
	UsageAsBorrower = "borrower"
	UsageAsTenant   = "tenant"
)

// BorrowUsageRow is one (period, owner, borrower, provider, room) bucket of
// recorded borrow turns. Cost sums the turns that reported one; it is absent
// when none did. InputTokens / OutputTokens likewise sum the turns that
// reported token usage and are absent when none did.
type BorrowUsageRow struct {
	Period       string   `json:"period"`
	Owner        string   `json:"owner"`
	Borrower     string   `json:"borrower"`
	Provider     string   `json:"provider"`
	RoomID       string   `json:"roomId"`
	Runs         int      `json:"runs"`
	Cost         *float64 `json:"cost,omitempty"`
	InputTokens  *int64   `json:"inputTokens,omitempty"`
	OutputTokens *int64   `json:"outputTokens,omitempty"`
}

// BorrowUsageReport is GET /v1/borrow/usage-report (and the tenant variant).
// Truncated is set when the scan hit its row cap.
type BorrowUsageReport struct {
	As        string           `json:"as" tstype:"UsageAs"`
	GroupBy   string           `json:"groupBy" tstype:"UsageGroupBy"`
	FromMs    int64            `json:"fromMs"`
	ToMs      int64            `json:"toMs"`
	Rows      []BorrowUsageRow `json:"rows"`
	TotalRuns int              `json:"totalRuns"`
	TotalCost *float64         `json:"totalCost,omitempty"`
	// TotalInputTokens / TotalOutputTokens are absent when no turn in the
	// report carried token usage.
	TotalInputTokens  *int64 `json:"totalInputTokens,omitempty"`
	TotalOutputTokens *int64 `json:"totalOutputTokens,omitempty"`
	Truncated         bool   `json:"truncated,omitempty"`
}

// Quota request statuses.
//
//wire:union QuotaRequestStatus
const (
	QuotaPending  = "pending"
	QuotaApproved = "approved"
	QuotaDeclined = "declined"
)

// QuotaRequest is one borrower's request for more runs on an owner's room
// authorization of Provider (the grant discriminator: "<core>:<provider>" or a
// bare provider).
type QuotaRequest struct {
	RequestID     string `json:"requestId"`
	RoomID        string `json:"roomId"`
	Owner         string `json:"owner"`
	Requester     string `json:"requester"`
	Provider      string `json:"provider"`
	RequestedRuns *int   `json:"requestedRuns,omitempty"`
	Note          string `json:"note,omitempty"`
	Status        string `json:"status" tstype:"QuotaRequestStatus"`
	GrantedRuns   *int   `json:"grantedRuns,omitempty"`
	CreatedAtMs   int64  `json:"createdAtMs"`
	DecidedAtMs   int64  `json:"decidedAtMs,omitempty"`
}

// CreateQuotaRequestInput is the POST /v1/borrow/quota-requests body. The
// requester is always the authenticated caller, never a body field.
type CreateQuotaRequestInput struct {
	RoomID        string `json:"roomId"`
	Owner         string `json:"owner"`
	Provider      string `json:"provider"`
	RequestedRuns *int   `json:"requestedRuns,omitempty"`
	Note          string `json:"note"`
}

// ApproveQuotaRequestInput is the optional approve body: Runs overrides the
// requested amount (the owner may grant fewer or more).
type ApproveQuotaRequestInput struct {
	Runs *int `json:"runs,omitempty"`
}
