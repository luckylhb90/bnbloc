package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Usage budgets (R12). Org admins set monthly token/cost budgets per member
// and for the tenant (GET/PUT /v1/tenant/budgets, manage_org at the
// gateway); members read their own (GET /v1/my/budget). The burn is the
// reported reply usage the team dashboard aggregates (AgentUsageReader) over
// the UTC calendar month, attributed to the agent's OWNER: a borrowed run
// consumes the owner's agent, so it counts against the owner's budget.
//
//   - Warnings: after replies with reported usage are ingested, the owner's
//     budget and the tenant budget are re-evaluated (debounced); crossing
//     warnAtPercent and then 100% each send one budget_warning notification
//     per month (to the member, or to the org admins for the tenant budget).
//     The once-per-threshold record is the budget row itself
//     (UsageBudgetStore.ClaimBudgetWarning), so replicas never double-send.
//   - Hard stop: the job claim — the call every bridge makes before it drives
//     a job — refuses the jobs of an owner whose hard-stop budget (or the
//     tenant's) is reached: the job fails with budget_exhausted and the room
//     gets a visible notice. A budget read error lets the job run: budgets
//     are a cost control, not an access boundary, and an outage of the
//     budget read must not stop every agent.

// BudgetNotifier sends a budget warning (notify.Emitter implements it).
type BudgetNotifier interface {
	Emit(ctx context.Context, ev notify.Event) (bool, error)
}

// Budget bounds (sanity ceilings on admin input, not pricing data).
const (
	defaultWarnAtPercent = 80
	maxBudgetMembers     = 1000
	maxBudgetTokens      = int64(1_000_000_000_000_000)
	maxBudgetCost        = 1_000_000_000.0
	maxBudgetUserID      = 128
	// budgetDebounce coalesces a burst of replies into one evaluation.
	defaultBudgetDebounce = 5 * time.Second
	// maxPendingBudgetChecks bounds the debounce map.
	maxPendingBudgetChecks = 10_000
)

// Which budget refused a drive (wire.MyBudgetReply.BlockedBy).
const (
	budgetByMember = "member"
	budgetByTenant = "tenant"
)

func (s *Service) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// budgetPeriod is the UTC calendar month containing now.
func budgetPeriod(now time.Time) wire.BudgetPeriod {
	u := now.UTC()
	start := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	return wire.BudgetPeriod{
		Period:        start.Format("2006-01"),
		PeriodStartMs: start.UnixMilli(),
		PeriodEndMs:   start.AddDate(0, 1, 0).UnixMilli(),
	}
}

// periodBurn sums the reported usage since sinceMs for owner ("" = the whole
// tenant): the total, and per owner (keyed lower-case).
func (s *Service) periodBurn(ctx context.Context, owner string, sinceMs int64) (wire.UsageBurn, map[string]*wire.MemberBurn, error) {
	var total wire.UsageBurn
	byOwner := map[string]*wire.MemberBurn{}
	if s.Usage == nil {
		return total, byOwner, nil
	}
	usage, err := s.Usage.AgentUsage(ctx, owner, sinceMs)
	if err != nil {
		return total, byOwner, err
	}
	for _, u := range usage {
		tokens := u.InputTokens + u.OutputTokens
		total.Tokens += tokens
		total.Cost += u.CostUsd
		key := u.OwnerUserID
		m := byOwner[key]
		if m == nil {
			m = &wire.MemberBurn{UserID: u.OwnerUserID}
			byOwner[key] = m
		}
		m.Tokens += tokens
		m.Cost += u.CostUsd
	}
	return total, byOwner, nil
}

// budgetPercent is the higher of the token and cost burn percentages
// (floored), and whether either limit is reached.
func budgetPercent(b store.UsageBudget, burn wire.UsageBurn) (int, bool) {
	percent, reached := 0, false
	if b.Tokens != nil && *b.Tokens > 0 {
		percent = max(percent, int(min(burn.Tokens*100 / *b.Tokens, math.MaxInt32)))
		reached = reached || burn.Tokens >= *b.Tokens
	}
	if b.Cost != nil && *b.Cost > 0 {
		percent = max(percent, int(math.Min(math.Floor(burn.Cost*100 / *b.Cost), math.MaxInt32)))
		reached = reached || burn.Cost >= *b.Cost
	}
	return percent, reached
}

func budgetView(b store.UsageBudget, burn wire.UsageBurn) wire.BudgetView {
	percent, reached := budgetPercent(b, burn)
	return wire.BudgetView{
		UserID: b.UserID,
		UsageBudgetLimits: wire.UsageBudgetLimits{
			Tokens: b.Tokens, Cost: b.Cost, WarnAtPercent: b.WarnAtPercent, HardStop: b.HardStop,
		},
		Burn: burn, Percent: percent, Reached: reached,
		UpdatedBy: b.UpdatedBy, UpdatedAtMs: b.UpdatedAtMs,
	}
}

// burnOf returns owner's entry of a periodBurn map (zero when absent).
func burnOf(byOwner map[string]*wire.MemberBurn, owner string) wire.UsageBurn {
	if m := byOwner[owner]; m != nil {
		return m.UsageBurn
	}
	return wire.UsageBurn{}
}

// tenantBudgets builds the admin view of the ctx tenant's budgets.
func (s *Service) tenantBudgets(ctx context.Context) (wire.TenantBudgetsReply, error) {
	period := budgetPeriod(s.clock())
	budgets, err := s.Budgets.ListUsageBudgets(ctx)
	if err != nil {
		return wire.TenantBudgetsReply{}, err
	}
	total, byOwner, err := s.periodBurn(ctx, "", period.PeriodStartMs)
	if err != nil {
		return wire.TenantBudgetsReply{}, err
	}
	out := wire.TenantBudgetsReply{BudgetPeriod: period, Members: []wire.BudgetView{}, Usage: []wire.MemberBurn{}}
	for _, b := range budgets {
		if b.UserID == "" {
			v := budgetView(b, total)
			out.Tenant = &v
			continue
		}
		out.Members = append(out.Members, budgetView(b, burnOf(byOwner, b.UserID)))
	}
	for _, m := range byOwner {
		out.Usage = append(out.Usage, *m)
	}
	sortMemberBurn(out.Usage)
	return out, nil
}

func sortMemberBurn(ms []wire.MemberBurn) {
	// Highest burn first (tokens, then cost), then by id: stable output.
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && burnLess(ms[j-1], ms[j]); j-- {
			ms[j-1], ms[j] = ms[j], ms[j-1]
		}
	}
}

func burnLess(a, b wire.MemberBurn) bool {
	if a.Tokens != b.Tokens {
		return a.Tokens < b.Tokens
	}
	if a.Cost != b.Cost {
		return a.Cost < b.Cost
	}
	return a.UserID > b.UserID
}

// handleTenantBudgets is GET /v1/tenant/budgets.
func (s *Service) handleTenantBudgets(w http.ResponseWriter, r *http.Request) {
	if !s.budgetsReady(w, r) {
		return
	}
	out, err := s.tenantBudgets(r.Context())
	if err != nil {
		obs.ServerError(w, "budgets read failed", err)
		httpx.Error(w, "budgets read failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handlePutTenantBudgets is PUT /v1/tenant/budgets: it replaces the ctx
// tenant's whole budget set and answers the new admin view.
func (s *Service) handlePutTenantBudgets(w http.ResponseWriter, r *http.Request) {
	if !s.budgetsReady(w, r) {
		return
	}
	var in wire.TenantBudgetsUpdate
	if !httpx.DecodeJSON(w, r, maxBodyBytes, &in) {
		return
	}
	caller := svcauth.Subject(r)
	budgets, err := s.validateBudgets(r.Context(), in, caller, s.clock().UnixMilli())
	var readErr budgetReadError
	if errors.As(err, &readErr) {
		obs.ServerError(w, "budgets membership read failed", readErr.err)
		httpx.Error(w, "budgets membership read failed", http.StatusInternalServerError)
		return
	}
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Budgets.ReplaceUsageBudgets(r.Context(), budgets); err != nil {
		obs.ServerError(w, "budgets write failed", err)
		httpx.Error(w, "budgets write failed", http.StatusInternalServerError)
		return
	}
	out, err := s.tenantBudgets(r.Context())
	if err != nil {
		obs.ServerError(w, "budgets read failed", err)
		httpx.Error(w, "budgets read failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	// A changed budget may already be crossed: evaluate everyone now.
	tid := tenant.FromContext(r.Context())
	for _, b := range budgets {
		if b.UserID != "" {
			s.noteBudgetBurn(tid, b.UserID)
		}
	}
	if len(budgets) > 0 && budgets[0].UserID == "" {
		s.noteBudgetBurn(tid, "")
	}
}

// budgetReadError is validateBudgets' store failure, kept apart from the
// caller's validation errors (a 500, not a 400).
type budgetReadError struct{ err error }

func (e budgetReadError) Error() string { return e.err.Error() }

// validateBudgets checks a PUT body and returns the store rows (the tenant
// budget first). A member budget must name a member of the tenant when the
// tenant registry is wired.
func (s *Service) validateBudgets(ctx context.Context, in wire.TenantBudgetsUpdate, caller string, nowMs int64) ([]store.UsageBudget, error) {
	if len(in.Members) > maxBudgetMembers {
		return nil, fmt.Errorf("at most %d member budgets", maxBudgetMembers)
	}
	var out []store.UsageBudget
	if in.Tenant != nil {
		b, err := budgetLimits(*in.Tenant)
		if err != nil {
			return nil, fmt.Errorf("tenant budget: %w", err)
		}
		out = append(out, b)
	}
	seen := map[string]bool{}
	tid := tenant.FromContext(ctx)
	for _, m := range in.Members {
		user := strings.TrimSpace(m.UserID)
		if user == "" || len(user) > maxBudgetUserID {
			return nil, fmt.Errorf("member budget: userId required (at most %d characters)", maxBudgetUserID)
		}
		if seen[user] {
			return nil, fmt.Errorf("member budget: %s listed twice", user)
		}
		seen[user] = true
		if s.Tenants != nil {
			_, member, err := s.Tenants.GetMember(ctx, tid, user)
			if err != nil {
				return nil, budgetReadError{err}
			}
			if !member {
				return nil, fmt.Errorf("member budget: %s is not a member of this organization", user)
			}
		}
		b, err := budgetLimits(m.UsageBudgetLimits)
		if err != nil {
			return nil, fmt.Errorf("member budget %s: %w", user, err)
		}
		b.UserID = user
		out = append(out, b)
	}
	for i := range out {
		out[i].UpdatedBy, out[i].UpdatedAtMs = caller, nowMs
	}
	return out, nil
}

// budgetLimits validates one budget's limits.
func budgetLimits(l wire.UsageBudgetLimits) (store.UsageBudget, error) {
	if l.Tokens == nil && l.Cost == nil {
		return store.UsageBudget{}, fmt.Errorf("set a token or cost limit")
	}
	if l.Tokens != nil && (*l.Tokens < 1 || *l.Tokens > maxBudgetTokens) {
		return store.UsageBudget{}, fmt.Errorf("tokens must be between 1 and %d", maxBudgetTokens)
	}
	// NaN fails every comparison, so test the admissible range positively.
	if l.Cost != nil && !(*l.Cost > 0 && *l.Cost <= maxBudgetCost) {
		return store.UsageBudget{}, fmt.Errorf("cost must be greater than 0 and at most %.0f", maxBudgetCost)
	}
	warn := l.WarnAtPercent
	if warn == 0 {
		warn = defaultWarnAtPercent
	}
	if warn < 1 || warn > 99 {
		return store.UsageBudget{}, fmt.Errorf("warnAtPercent must be between 1 and 99")
	}
	return store.UsageBudget{Tokens: l.Tokens, Cost: l.Cost, WarnAtPercent: warn, HardStop: l.HardStop}, nil
}

// handleMyBudget is GET /v1/my/budget: the caller's own budget and burn this
// month, and whether a reached hard-stop budget refuses their agents' drives.
func (s *Service) handleMyBudget(w http.ResponseWriter, r *http.Request) {
	if !s.budgetsReady(w, r) {
		return
	}
	caller := svcauth.Subject(r)
	period := budgetPeriod(s.clock())
	burn, _, err := s.periodBurn(r.Context(), caller, period.PeriodStartMs)
	if err != nil {
		obs.ServerError(w, "usage read failed", err)
		httpx.Error(w, "budget read failed", http.StatusInternalServerError)
		return
	}
	out := wire.MyBudgetReply{BudgetPeriod: period, Burn: burn}
	b, ok, err := s.Budgets.GetUsageBudget(r.Context(), caller)
	if err != nil {
		obs.ServerError(w, "budget read failed", err)
		httpx.Error(w, "budget read failed", http.StatusInternalServerError)
		return
	}
	if ok {
		v := budgetView(b, burn)
		out.Budget = &v
	}
	out.BlockedBy = s.budgetBlock(r.Context(), caller, period.PeriodStartMs)
	out.Blocked = out.BlockedBy != ""
	httpx.WriteJSON(w, http.StatusOK, out)
}

// budgetsReady answers 401 / 501 when the request cannot be served.
func (s *Service) budgetsReady(w http.ResponseWriter, r *http.Request) bool {
	if svcauth.Subject(r) == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return false
	}
	if s.Budgets == nil {
		httpx.Unavailable(w, "usage budgets")
		return false
	}
	return true
}

// budgetBlock reports which reached hard-stop budget refuses owner's agent
// drives ("" = none): the owner's own, else the tenant's. A read error
// refuses nothing (see the package comment).
func (s *Service) budgetBlock(ctx context.Context, owner string, sinceMs int64) string {
	if s.Budgets == nil {
		return ""
	}
	if b, ok, err := s.Budgets.GetUsageBudget(ctx, owner); err == nil && ok && b.HardStop {
		burn, _, err := s.periodBurn(ctx, owner, sinceMs)
		if err != nil {
			slog.Warn("bnbloc activity usage read failed", "event", "bnbloc.activity.budgets_failed", "owner", owner, "error", err)
		} else if _, reached := budgetPercent(b, burn); reached {
			return budgetByMember
		}
	} else if err != nil {
		slog.Warn("bnbloc activity budget read failed", "event", "bnbloc.activity.budgets_failed", "owner", owner, "error", err)
	}
	if b, ok, err := s.Budgets.GetUsageBudget(ctx, ""); err == nil && ok && b.HardStop {
		burn, _, err := s.periodBurn(ctx, "", sinceMs)
		if err != nil {
			slog.Warn("bnbloc activity usage read failed", "event", "bnbloc.activity.budgets_failed", "error", err)
		} else if _, reached := budgetPercent(b, burn); reached {
			return budgetByTenant
		}
	} else if err != nil {
		slog.Warn("bnbloc activity budget read failed", "event", "bnbloc.activity.budgets_failed", "error", err)
	}
	return ""
}

// refuseOverBudget fails, at claim time, the claimed jobs whose owner a
// reached hard-stop budget stops, and returns the jobs the bridge may drive.
// A refused job gets a visible room notice. A job that cannot be failed
// (store error) is handed to the bridge rather than stranded.
func (s *Service) refuseOverBudget(ctx context.Context, bridgeID string, jobs []store.AgentJob) []store.AgentJob {
	if s.Budgets == nil || len(jobs) == 0 {
		return jobs
	}
	now := s.clock()
	since := budgetPeriod(now).PeriodStartMs
	blocked := map[string]string{}
	kept := make([]store.AgentJob, 0, len(jobs))
	for _, j := range jobs {
		key := j.OwnerUserID
		by, seen := blocked[key]
		if !seen {
			by = s.budgetBlock(ctx, j.OwnerUserID, since)
			blocked[key] = by
		}
		if by == "" {
			kept = append(kept, j)
			continue
		}
		diag, _ := json.Marshal(map[string]any{"budget": by, "period": budgetPeriod(now).Period})
		failed, err := s.BridgeJobs.UpdateAgentJobStatus(ctx, store.JobStatusUpdate{
			JobID: j.JobID, BridgeID: bridgeID, Status: store.AgentJobFailed, FailureReason: store.FailureBudgetExhausted,
			DiagnosticsJSON: diag, NowMs: now.UnixMilli(), LeaseMs: store.DefaultLeaseMs, MaxAttempts: store.DefaultMaxJobAttempts,
		})
		if err != nil {
			slog.Warn("bnbloc activity budget refusal not recorded; job handed to the bridge",
				"event", "bnbloc.activity.budget_refusal_failed", "job", j.JobID, "error", err)
			kept = append(kept, j)
			continue
		}
		s.publishJob(ctx, failed, now.UnixMilli())
		s.postJobNotice(ctx, failed, budgetRefusalText(failed, by), now.UnixMilli())
	}
	return kept
}

// budgetRefusalText explains a budget refusal in the room.
func budgetRefusalText(job store.AgentJob, by string) string {
	agent := strings.TrimSpace(job.Provider)
	if agent == "" {
		agent = "agent"
	}
	if by == budgetByTenant {
		return "The organization's monthly budget reached: the " + agent + " agent did not run. Agents run again next month or when an admin raises the budget."
	}
	return job.OwnerUserID + "'s monthly budget reached: the " + agent + " agent did not run. It runs again next month or when an admin raises the budget."
}

// budgetWatch debounces warning evaluations per (tenant, owner).
type budgetWatch struct {
	mu      sync.Mutex
	pending map[string]bool
}

// noteBudgetBurn schedules a warning evaluation of owner's budget and the
// tenant budget (owner "" = the tenant budget only). Bursts of replies
// coalesce into one evaluation after the debounce; it never blocks the
// caller.
func (s *Service) noteBudgetBurn(tenantID, owner string) {
	if s.Budgets == nil || s.Notifications == nil {
		return
	}
	key := tenantID + "\x00" + owner
	s.budgetWatch.mu.Lock()
	if s.budgetWatch.pending == nil {
		s.budgetWatch.pending = map[string]bool{}
	}
	if s.budgetWatch.pending[key] || len(s.budgetWatch.pending) >= maxPendingBudgetChecks {
		s.budgetWatch.mu.Unlock()
		return
	}
	s.budgetWatch.pending[key] = true
	s.budgetWatch.mu.Unlock()
	delay := s.BudgetDebounce
	if delay <= 0 {
		delay = defaultBudgetDebounce
	}
	time.AfterFunc(delay, func() {
		s.budgetWatch.mu.Lock()
		delete(s.budgetWatch.pending, key)
		s.budgetWatch.mu.Unlock()
		ctx, cancel := context.WithTimeout(tenant.NewContext(context.Background(), tenantID), 30*time.Second)
		defer cancel()
		s.EvaluateBudgetWarnings(ctx, owner)
	})
}

// EvaluateBudgetWarnings sends the budget warnings owner's budget and the
// ctx tenant's budget are due (owner "" = the tenant budget only).
func (s *Service) EvaluateBudgetWarnings(ctx context.Context, owner string) {
	if s.Budgets == nil || s.Notifications == nil {
		return
	}
	period := budgetPeriod(s.clock())
	if owner != "" {
		if b, ok, err := s.Budgets.GetUsageBudget(ctx, owner); err == nil && ok {
			if burn, _, err := s.periodBurn(ctx, owner, period.PeriodStartMs); err != nil {
				slog.Warn("bnbloc activity usage read failed; budget warning skipped", "event", "bnbloc.activity.budgets_failed", "owner", owner, "error", err)
			} else {
				s.warnBudget(ctx, b, burn, period, []string{owner})
			}
		}
	}
	if b, ok, err := s.Budgets.GetUsageBudget(ctx, ""); err == nil && ok {
		burn, _, err := s.periodBurn(ctx, "", period.PeriodStartMs)
		if err != nil {
			slog.Warn("bnbloc activity usage read failed; budget warning skipped", "event", "bnbloc.activity.budgets_failed", "error", err)
			return
		}
		admins, err := s.orgAdmins(ctx)
		if err != nil {
			slog.Warn("bnbloc activity org admins read failed; budget warning skipped", "event", "bnbloc.activity.budgets_failed", "error", err)
			return
		}
		s.warnBudget(ctx, b, burn, period, admins)
	}
}

// warnBudget sends the highest warning level b's burn has crossed, once per
// period: crossing straight to 100% sends only the 100% warning.
func (s *Service) warnBudget(ctx context.Context, b store.UsageBudget, burn wire.UsageBurn, period wire.BudgetPeriod, recipients []string) {
	percent, reached := budgetPercent(b, burn)
	level := 0
	switch {
	case reached:
		level = 100
	case percent >= b.WarnAtPercent:
		level = b.WarnAtPercent
	default:
		return
	}
	if len(recipients) == 0 {
		return
	}
	won, err := s.Budgets.ClaimBudgetWarning(ctx, b.UserID, period.Period, level)
	if err != nil || !won {
		return
	}
	ev := budgetWarningEvent(b, burn, period, level, percent, recipients, s.PublicURL)
	if _, err := s.Notifications.Emit(ctx, ev); err != nil {
		slog.Warn("bnbloc activity budget warning not queued",
			"event", "bnbloc.notify.enqueue_failed", "type", ev.Type, "user", b.UserID, "error", err)
	}
}

// budgetWarningEvent renders one budget warning.
func budgetWarningEvent(b store.UsageBudget, burn wire.UsageBurn, period wire.BudgetPeriod, level, percent int, recipients []string, publicURL string) notify.Event {
	whose, link := "Your agents", "/#/settings"
	if b.UserID == "" {
		whose, link = "Your organization's agents", "/#/team"
	}
	title := whose + " used " + strconv.Itoa(percent) + "% of this month's budget"
	if level >= 100 {
		title = whose + " reached this month's budget"
	}
	var parts []string
	if b.Tokens != nil {
		parts = append(parts, "Tokens: "+strconv.FormatInt(burn.Tokens, 10)+" of "+strconv.FormatInt(*b.Tokens, 10)+".")
	}
	if b.Cost != nil {
		parts = append(parts, fmt.Sprintf("Cost: %.2f of %.2f.", burn.Cost, *b.Cost))
	}
	switch {
	case level >= 100 && b.HardStop:
		parts = append(parts, "Agent runs are stopped until next month or until an admin raises the budget.")
	case level >= 100:
		parts = append(parts, "Agents keep running; usage beyond the budget is reported to admins.")
	}
	url := ""
	if p := strings.TrimRight(strings.TrimSpace(publicURL), "/"); p != "" {
		url = p + link
	}
	subject := b.UserID
	if subject == "" {
		subject = "tenant"
	}
	return notify.Event{
		Type: wire.NotificationBudgetWarning, Recipients: recipients,
		Title: title, Text: strings.Join(parts, " "), URL: url,
		DedupKey: "budget:" + subject + ":" + period.Period + ":" + strconv.Itoa(level),
	}
}

// orgAdmins lists the ctx tenant's org admins (tenant budget recipients).
func (s *Service) orgAdmins(ctx context.Context) ([]string, error) {
	if s.Tenants == nil {
		return nil, nil
	}
	members, err := s.Tenants.ListMembers(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range members {
		if m.Role == store.OrgRoleAdmin {
			out = append(out, m.UserID)
		}
	}
	return out, nil
}

// noteReplyBurn schedules budget evaluations for the owners whose freshly
// stored replies reported usage.
func (s *Service) noteReplyBurn(ctx context.Context, fresh []wire.Message) {
	if s.Budgets == nil || s.Notifications == nil {
		return
	}
	tid := tenant.FromContext(ctx)
	seen := map[string]bool{}
	for _, m := range fresh {
		u, ok := store.ReplyUsageOf(m.Payload)
		if !ok {
			continue
		}
		if owner := strings.TrimSpace(u.Owner); owner != "" && !seen[owner] {
			seen[owner] = true
			s.noteBudgetBurn(tid, owner)
		}
	}
}
