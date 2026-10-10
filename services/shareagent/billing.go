package shareagenthttp

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Borrow billing (F3): usage reports over the recorded borrow turns (the
// shareagent audit trail — allowed "turn" records whose actor is not the
// owner), grouped by day or month, as JSON or CSV.
//
//   - GET /v1/borrow/usage-report?as=owner|borrower — the caller's side only:
//     as=owner reports borrows OF the caller's agents, as=borrower the
//     caller's borrows of others' agents. The caller is X-Bnbloc-User.
//   - GET /v1/tenant/borrow/usage-report — every borrow in the ctx tenant,
//     behind the gateway's manage_org gate on /v1/tenant/*.
//
// Query: from/to (unix ms or YYYY-MM-DD; to is exclusive; default = this
// month so far), groupBy=day|month (default month), tzOffsetMin (the browser's
// Date.getTimezoneOffset(), used for date parsing and period labels; default
// UTC), format=csv for a download.

// maxReportSpan bounds one report's time range.
const maxReportSpan = 400 * 24 * time.Hour

// reportParams is a parsed usage-report query.
type reportParams struct {
	from, to time.Time
	groupBy  string
	loc      *time.Location
	csv      bool
}

func parseReportParams(r *http.Request, now time.Time) (reportParams, string) {
	q := r.URL.Query()
	p := reportParams{groupBy: q.Get("groupBy"), csv: q.Get("format") == "csv", loc: time.UTC}
	if p.groupBy == "" {
		p.groupBy = wire.UsageGroupMonth
	}
	if p.groupBy != wire.UsageGroupDay && p.groupBy != wire.UsageGroupMonth {
		return p, "groupBy must be day or month"
	}
	if f := q.Get("format"); f != "" && f != "csv" && f != "json" {
		return p, "format must be json or csv"
	}
	var bad string
	if p.loc, bad = reportLocation(q.Get("tzOffsetMin")); bad != "" {
		return p, bad
	}
	local := now.In(p.loc)
	p.from = time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, p.loc)
	p.to = now
	var ok bool
	if v := q.Get("from"); v != "" {
		if p.from, ok = parseReportTime(v, p.loc); !ok {
			return p, "from must be unix ms or YYYY-MM-DD"
		}
	}
	if v := q.Get("to"); v != "" {
		if p.to, ok = parseReportTime(v, p.loc); !ok {
			return p, "to must be unix ms or YYYY-MM-DD"
		}
	}
	if !p.to.After(p.from) {
		return p, "to must be after from"
	}
	if p.to.Sub(p.from) > maxReportSpan {
		return p, "range must not exceed 400 days"
	}
	return p, ""
}

// reportLocation parses tzOffsetMin (the browser's
// Date.getTimezoneOffset()); empty is UTC.
func reportLocation(tz string) (*time.Location, string) {
	if tz == "" {
		return time.UTC, ""
	}
	n, err := strconv.Atoi(tz)
	if err != nil || n < -14*60 || n > 14*60 {
		return time.UTC, "tzOffsetMin must be within ±840"
	}
	return time.FixedZone("client", -n*60), ""
}

func parseReportTime(v string, loc *time.Location) (time.Time, bool) {
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms >= 0 {
		return time.UnixMilli(ms), true
	}
	t, err := time.ParseInLocation("2006-01-02", v, loc)
	return t, err == nil
}

func (p reportParams) period(atMs int64) string {
	t := time.UnixMilli(atMs).In(p.loc)
	if p.groupBy == wire.UsageGroupDay {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01")
}

// handleUsageReport serves the caller-scoped report (as=owner|borrower).
func (s *Service) handleUsageReport(w http.ResponseWriter, r *http.Request) {
	caller := callerSubject(r)
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	as := r.URL.Query().Get("as")
	if as == "" {
		as = wire.UsageAsOwner
	}
	q := store.BorrowTurnQuery{}
	switch as {
	case wire.UsageAsOwner:
		q.OwnerUserID = caller
	case wire.UsageAsBorrower:
		q.BorrowerUserID = caller
	default:
		writeErr(w, http.StatusBadRequest, "as must be owner or borrower")
		return
	}
	s.serveUsageReport(w, r, as, q)
}

// handleTenantUsageReport serves the tenant-wide report (manage_org at the
// gateway).
func (s *Service) handleTenantUsageReport(w http.ResponseWriter, r *http.Request) {
	if callerSubject(r) == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	s.serveUsageReport(w, r, wire.UsageAsTenant, store.BorrowTurnQuery{})
}

func (s *Service) serveUsageReport(w http.ResponseWriter, r *http.Request, as string, q store.BorrowTurnQuery) {
	p, bad := parseReportParams(r, time.UnixMilli(s.Now()))
	if bad != "" {
		writeErr(w, http.StatusBadRequest, bad)
		return
	}
	q.FromMs, q.ToMs = p.from.UnixMilli(), p.to.UnixMilli()
	turns := s.Grants.BorrowTurns(r.Context(), q)
	report := wire.BorrowUsageReport{
		As:        as,
		GroupBy:   p.groupBy,
		FromMs:    q.FromMs,
		ToMs:      q.ToMs,
		Rows:      s.groupBorrowTurns(r.Context(), p, turns),
		Truncated: len(turns) >= store.MaxBorrowTurnRows,
	}
	for _, row := range report.Rows {
		report.TotalRuns += row.Runs
		report.TotalCost = addReported(report.TotalCost, row.Cost)
		report.TotalInputTokens = addReported(report.TotalInputTokens, row.InputTokens)
		report.TotalOutputTokens = addReported(report.TotalOutputTokens, row.OutputTokens)
	}
	if p.csv {
		writeUsageCSV(w, report)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// groupBorrowTurns buckets turns by (period, owner, borrower, provider, room).
// The provider is the grant's target discriminator ("<core>:<provider>"),
// resolved once per grant.
func (s *Service) groupBorrowTurns(ctx context.Context, p reportParams, turns []store.AuditRecord) []wire.BorrowUsageRow {
	providers := map[string]string{}
	provider := func(grantID string) string {
		if v, ok := providers[grantID]; ok {
			return v
		}
		v := ""
		if g, err := s.Grants.GetGrant(ctx, grantID); err == nil {
			v = providerFromTarget(g.TargetID)
		}
		providers[grantID] = v
		return v
	}
	rows := map[string]*wire.BorrowUsageRow{}
	for _, t := range turns {
		row := wire.BorrowUsageRow{
			Period:   p.period(t.AtMs),
			Owner:    t.OwnerUserID,
			Borrower: t.ActorUserID,
			Provider: provider(t.GrantID),
			RoomID:   t.RoomID,
		}
		k := strings.Join([]string{row.Period, strings.ToLower(row.Owner), strings.ToLower(row.Borrower), row.Provider, row.RoomID}, "\x00")
		acc := rows[k]
		if acc == nil {
			acc = &row
			rows[k] = acc
		}
		acc.Runs++
		acc.Cost = addReported(acc.Cost, t.Cost)
		acc.InputTokens = addReported(acc.InputTokens, t.InputTokens)
		acc.OutputTokens = addReported(acc.OutputTokens, t.OutputTokens)
	}
	out := make([]wire.BorrowUsageRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, k int) bool {
		a, b := out[i], out[k]
		if a.Period != b.Period {
			return a.Period > b.Period // newest period first
		}
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		if a.Borrower != b.Borrower {
			return a.Borrower < b.Borrower
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.RoomID < b.RoomID
	})
	return out
}

// addReported sums an optional (reported-only) amount: nil + nil stays nil,
// so a total over turns that reported nothing is absent rather than zero.
func addReported[T int64 | float64](acc, v *T) *T {
	if v == nil {
		return acc
	}
	sum := *v
	if acc != nil {
		sum += *acc
	}
	return &sum
}

// csvCell neutralizes spreadsheet formula injection: a member-chosen id or
// room name starting with a formula character is prefixed with a quote.
func csvCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// csvInt renders an optional count; empty when not reported (never "0").
func csvInt(v *int64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatInt(*v, 10)
}

func writeUsageCSV(w http.ResponseWriter, report wire.BorrowUsageReport) {
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"period", "owner", "borrower", "provider", "room_id", "runs", "cost", "input_tokens", "output_tokens"})
	for _, row := range report.Rows {
		cost := ""
		if row.Cost != nil {
			cost = strconv.FormatFloat(*row.Cost, 'f', -1, 64)
		}
		_ = cw.Write([]string{row.Period, csvCell(row.Owner), csvCell(row.Borrower), csvCell(row.Provider),
			csvCell(row.RoomID), strconv.Itoa(row.Runs), cost, csvInt(row.InputTokens), csvInt(row.OutputTokens)})
	}
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="borrow-usage-`+report.As+`-`+report.GroupBy+`.csv"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
