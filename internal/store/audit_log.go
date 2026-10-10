package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// Audit log (R13): one read side over the append-only audit trails —
// extension_audit (component lifecycle, plugin/notifier deliveries, API
// tokens, admin alerts, notifier replies, member/tenant administration,
// session revocations) and shared_agent_audit (borrow decisions). Each trail
// answers the same query in the same total order so a caller can merge them
// with one cursor:
//
//	at_ms DESC, source rank ASC, row id DESC
//
// Rows carry an explicit tenant filter rather than the ctx tenant because the
// platform (super-admin) view spans tenants; the caller decides the scope and
// a tenant-scoped caller always sets TenantID.

// Audit log sources, in merge rank order.
const (
	AuditSourceExtension = "extension"
	AuditSourceBorrow    = "borrow"
)

// AuditKindBorrow is the kind every shared_agent_audit row is reported as.
const AuditKindBorrow = "borrow"

func auditSourceRank(source string) int {
	if source == AuditSourceBorrow {
		return 1
	}
	return 0
}

// AuditCursor is the position of the last row a page returned.
type AuditCursor struct {
	AtMs   int64
	Source string
	ID     int64
}

// AuditLogQuery filters one audit log page.
type AuditLogQuery struct {
	// TenantID scopes rows to one tenant; "" with AllTenants spans every
	// tenant (platform view) and also includes tenantless platform rows.
	// "" without AllTenants matches nothing (fail closed).
	TenantID   string
	AllTenants bool
	// Kinds keeps only rows of these kinds (empty = all).
	Kinds []string
	// Actor and Outcome are exact matches (empty = any).
	Actor   string
	Outcome string
	// FromMs (inclusive) and ToMs (exclusive) bound at_ms (0 = open).
	FromMs, ToMs int64
	// After continues after the last row of a previous page (nil = first).
	After *AuditCursor
	// Limit is the page size the reader returns at most.
	Limit int
}

// AuditLogRow is one normalized audit record.
type AuditLogRow struct {
	Source   string
	ID       int64
	Kind     string
	Name     string
	Action   string
	Actor    string
	TenantID string
	Outcome  string
	Detail   string
	RoomID   string
	AtMs     int64
}

// Cursor is the row's merge position.
func (r AuditLogRow) Cursor() AuditCursor {
	return AuditCursor{AtMs: r.AtMs, Source: r.Source, ID: r.ID}
}

// AuditLogReader is one audit trail's read side.
type AuditLogReader interface {
	AuditLog(ctx context.Context, q AuditLogQuery) ([]AuditLogRow, error)
}

var (
	_ AuditLogReader = (*MemoryExtensionAudit)(nil)
	_ AuditLogReader = (*MySQLExtensionAudit)(nil)
	_ AuditLogReader = (*MemoryGrant)(nil)
	_ AuditLogReader = (*MySQLGrant)(nil)
)

// auditBefore reports whether a sorts strictly before b in merge order.
func auditBefore(a, b AuditCursor) bool {
	if a.AtMs != b.AtMs {
		return a.AtMs > b.AtMs
	}
	if ra, rb := auditSourceRank(a.Source), auditSourceRank(b.Source); ra != rb {
		return ra < rb
	}
	return a.ID > b.ID
}

// MergeAuditLog reads one page across readers in merge order. next is the
// cursor of the page's last row when more rows follow, nil at the end.
func MergeAuditLog(ctx context.Context, q AuditLogQuery, readers ...AuditLogReader) (rows []AuditLogRow, next *AuditCursor, err error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	page := q.Limit
	// One extra row per reader tells whether anything follows the page.
	q.Limit = page + 1
	for _, rd := range readers {
		if rd == nil {
			continue
		}
		got, err := rd.AuditLog(ctx, q)
		if err != nil {
			return nil, nil, err
		}
		rows = append(rows, got...)
	}
	sort.SliceStable(rows, func(i, j int) bool { return auditBefore(rows[i].Cursor(), rows[j].Cursor()) })
	if len(rows) > page {
		rows = rows[:page]
		c := rows[page-1].Cursor()
		next = &c
	}
	return rows, next, nil
}

// wantsKind reports whether the kind filter admits kind.
func (q AuditLogQuery) wantsKind(kind string) bool {
	if len(q.Kinds) == 0 {
		return true
	}
	for _, k := range q.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// wantsBorrow reports whether the borrow trail can contribute any row.
func (q AuditLogQuery) wantsBorrow() bool { return q.wantsKind(AuditKindBorrow) }

// scoped reports whether the query names any scope at all.
func (q AuditLogQuery) scoped() bool { return q.AllTenants || q.TenantID != "" }

// matches applies the query's filters to a normalized row (memory stores).
func (q AuditLogQuery) matches(r AuditLogRow) bool {
	if !q.AllTenants && r.TenantID != q.TenantID {
		return false
	}
	if !q.wantsKind(r.Kind) {
		return false
	}
	if q.Actor != "" && !strings.EqualFold(r.Actor, q.Actor) {
		return false
	}
	if q.Outcome != "" && !strings.EqualFold(r.Outcome, q.Outcome) {
		return false
	}
	if q.FromMs > 0 && r.AtMs < q.FromMs {
		return false
	}
	if q.ToMs > 0 && r.AtMs >= q.ToMs {
		return false
	}
	if q.After != nil && !auditBefore(*q.After, r.Cursor()) {
		return false
	}
	return true
}

// pageMemory sorts candidate rows in merge order and applies the limit.
func pageMemory(rows []AuditLogRow, limit int) []AuditLogRow {
	sort.SliceStable(rows, func(i, j int) bool { return auditBefore(rows[i].Cursor(), rows[j].Cursor()) })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// AuditLog implements AuditLogReader for the in-memory extension trail. The
// row id is the append position (1-based), monotonic like the MySQL id.
func (s *MemoryExtensionAudit) AuditLog(_ context.Context, q AuditLogQuery) ([]AuditLogRow, error) {
	if !q.scoped() {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AuditLogRow
	for i, rec := range s.recs {
		row := extAuditRow(int64(i+1), rec)
		if q.matches(row) {
			out = append(out, row)
		}
	}
	return pageMemory(out, q.Limit), nil
}

// AuditLog implements AuditLogReader for the in-memory borrow trail.
func (s *MemoryGrant) AuditLog(_ context.Context, q AuditLogQuery) ([]AuditLogRow, error) {
	if !q.scoped() || !q.wantsBorrow() {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AuditLogRow
	for i, rec := range s.audit {
		row := borrowAuditRow(int64(i+1), rec)
		if q.matches(row) {
			out = append(out, row)
		}
	}
	return pageMemory(out, q.Limit), nil
}

func extAuditRow(id int64, rec ExtAuditRecord) AuditLogRow {
	return AuditLogRow{
		Source: AuditSourceExtension, ID: id, Kind: rec.Kind, Name: rec.Name, Action: rec.Action,
		Actor: rec.Actor, TenantID: rec.TenantID, Outcome: rec.Outcome, Detail: rec.Detail, AtMs: rec.AtMs,
	}
}

// borrowAuditRow normalizes one borrow decision: the agent owner is the
// subject (Name), the decision the outcome.
func borrowAuditRow(id int64, rec AuditRecord) AuditLogRow {
	var detail []string
	if rec.DenyReason != "" {
		detail = append(detail, "reason="+rec.DenyReason)
	}
	if rec.Model != "" {
		detail = append(detail, "model="+rec.Model)
	}
	if rec.ChannelID != "" {
		detail = append(detail, "channel="+rec.ChannelID)
	}
	return AuditLogRow{
		Source: AuditSourceBorrow, ID: id, Kind: AuditKindBorrow, Name: rec.OwnerUserID, Action: rec.Action,
		Actor: rec.ActorUserID, TenantID: rec.TenantID, Outcome: rec.Decision, Detail: strings.Join(detail, " "),
		RoomID: rec.RoomID, AtMs: rec.AtMs,
	}
}

// auditSQLFilter builds the shared WHERE clause for one trail. rank is the
// trail's merge rank; cols names its tenant/actor/outcome/kind/id columns
// (kindCol "" = the trail has a single fixed kind, filtered by the caller).
func auditSQLFilter(q AuditLogQuery, rank int, tenantCol, actorCol, outcomeCol, kindCol, idCol string) (string, []any) {
	where := []string{"1=1"}
	var args []any
	if !q.AllTenants {
		where = append(where, tenantCol+" = ?")
		args = append(args, q.TenantID)
	}
	if kindCol != "" && len(q.Kinds) > 0 {
		where = append(where, kindCol+" IN (?"+strings.Repeat(",?", len(q.Kinds)-1)+")")
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	if q.Actor != "" {
		where = append(where, actorCol+" = ?")
		args = append(args, q.Actor)
	}
	if q.Outcome != "" {
		where = append(where, outcomeCol+" = ?")
		args = append(args, q.Outcome)
	}
	if q.FromMs > 0 {
		where = append(where, "at_ms >= ?")
		args = append(args, q.FromMs)
	}
	if q.ToMs > 0 {
		where = append(where, "at_ms < ?")
		args = append(args, q.ToMs)
	}
	if c := q.After; c != nil {
		cr := auditSourceRank(c.Source)
		switch {
		case rank > cr:
			// Every row at the cursor's timestamp follows it.
			where = append(where, "at_ms <= ?")
			args = append(args, c.AtMs)
		case rank == cr:
			where = append(where, "(at_ms < ? OR (at_ms = ? AND "+idCol+" < ?))")
			args = append(args, c.AtMs, c.AtMs, c.ID)
		default:
			where = append(where, "at_ms < ?")
			args = append(args, c.AtMs)
		}
	}
	return strings.Join(where, " AND "), args
}

func auditLimit(n int) int {
	if n <= 0 {
		return 100
	}
	return n
}

// AuditLog implements AuditLogReader over extension_audit
// (idx_extension_audit_tenant for a tenant scope, idx_extension_audit_at
// across tenants).
func (s *MySQLExtensionAudit) AuditLog(ctx context.Context, q AuditLogQuery) ([]AuditLogRow, error) {
	if !q.scoped() {
		return nil, nil
	}
	where, args := auditSQLFilter(q, 0, "tenant_id", "actor", "outcome", "kind", "id")
	args = append(args, auditLimit(q.Limit))
	rows, err := s.DB.QueryContext(ctx, `SELECT id, kind, name, action, actor, tenant_id, outcome, COALESCE(detail, ''), at_ms
FROM extension_audit WHERE `+where+` ORDER BY at_ms DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLogRow
	for rows.Next() {
		r := AuditLogRow{Source: AuditSourceExtension}
		if err := rows.Scan(&r.ID, &r.Kind, &r.Name, &r.Action, &r.Actor, &r.TenantID, &r.Outcome, &r.Detail, &r.AtMs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuditLog implements AuditLogReader over shared_agent_audit
// (idx_audit_tenant_time for a tenant scope, idx_audit_time across tenants).
func (s *MySQLGrant) AuditLog(ctx context.Context, q AuditLogQuery) ([]AuditLogRow, error) {
	if !q.scoped() || !q.wantsBorrow() {
		return nil, nil
	}
	where, args := auditSQLFilter(q, 1, "tenant_id", "actor_user_id", "decision", "", "audit_id")
	args = append(args, auditLimit(q.Limit))
	rows, err := s.DB.QueryContext(ctx, `SELECT audit_id, tenant_id, room_id, actor_user_id, owner_user_id, action, decision,
       COALESCE(deny_reason, ''), COALESCE(model, ''), channel_id, at_ms
FROM shared_agent_audit WHERE `+where+` ORDER BY at_ms DESC, audit_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLogRow
	for rows.Next() {
		var id int64
		var rec AuditRecord
		var channel sql.NullString
		if err := rows.Scan(&id, &rec.TenantID, &rec.RoomID, &rec.ActorUserID, &rec.OwnerUserID, &rec.Action, &rec.Decision,
			&rec.DenyReason, &rec.Model, &channel, &rec.AtMs); err != nil {
			return nil, err
		}
		rec.ChannelID = channel.String
		out = append(out, borrowAuditRow(id, rec))
	}
	return out, rows.Err()
}

// AuditLogReaders returns the configured audit trails' read sides in source
// order (nil entries are skipped by MergeAuditLog).
func (b *Backends) AuditLogReaders() []AuditLogReader {
	var out []AuditLogReader
	if r, ok := b.ExtAudit.(AuditLogReader); ok {
		out = append(out, r)
	}
	if r, ok := b.Grant.(AuditLogReader); ok {
		out = append(out, r)
	}
	return out
}
