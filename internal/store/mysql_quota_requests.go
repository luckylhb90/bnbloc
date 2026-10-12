package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/go-sql-driver/mysql"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// MySQLQuotaRequests is the MySQL QuotaRequestStore over
// shared_agent_quota_requests. The one-open-request rule is the unique
// pending_key (set while pending, NULL once decided).
type MySQLQuotaRequests struct {
	DB *sql.DB
}

// NewMySQLQuotaRequests constructs a MySQL-backed quota request store.
func NewMySQLQuotaRequests(db *sql.DB) *MySQLQuotaRequests { return &MySQLQuotaRequests{DB: db} }

var _ QuotaRequestStore = (*MySQLQuotaRequests)(nil)

const quotaRequestCols = `request_id, room_id, owner_user_id, requester_user_id, provider, requested_runs,
  note, status, granted_runs, created_at_ms, decided_at_ms, decided_by`

func scanQuotaRequest(scan func(...any) error, tnt string) (QuotaRequest, error) {
	var r QuotaRequest
	var requested, granted sql.NullInt64
	var status string
	if err := scan(&r.RequestID, &r.RoomID, &r.OwnerUserID, &r.RequesterUserID, &r.Provider, &requested,
		&r.Note, &status, &granted, &r.CreatedAtMs, &r.DecidedAtMs, &r.DecidedBy); err != nil {
		return QuotaRequest{}, err
	}
	r.TenantID = tnt
	r.Status = QuotaRequestStatus(status)
	if requested.Valid {
		v := int(requested.Int64)
		r.RequestedRuns = &v
	}
	if granted.Valid {
		v := int(granted.Int64)
		r.GrantedRuns = &v
	}
	return r, nil
}

// CreateQuotaRequest inserts a pending request; a duplicate pending_key returns
// the existing open request with ErrQuotaRequestPending.
func (s *MySQLQuotaRequests) CreateQuotaRequest(ctx context.Context, r QuotaRequest) (QuotaRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return QuotaRequest{}, err
	}
	r.TenantID = tnt
	r.Status = QuotaRequestPending
	pk := quotaPendingKey(tnt, r)
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO shared_agent_quota_requests
		 (request_id, tenant_id, room_id, owner_user_id, requester_user_id, provider, requested_runs,
		  note, status, pending_key, created_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		r.RequestID, tnt, r.RoomID, r.OwnerUserID, r.RequesterUserID, r.Provider, nullInt(r.RequestedRuns),
		r.Note, pk, r.CreatedAtMs)
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == mysqlErrDupEntry {
		existing, gerr := scanQuotaRequest(s.DB.QueryRowContext(ctx,
			`SELECT `+quotaRequestCols+` FROM shared_agent_quota_requests WHERE tenant_id = ? AND pending_key = ?`,
			tnt, pk).Scan, tnt)
		if gerr != nil {
			return QuotaRequest{}, gerr
		}
		return existing, ErrQuotaRequestPending
	}
	if err != nil {
		return QuotaRequest{}, err
	}
	return r, nil
}

// GetQuotaRequest returns one request in the ctx tenant.
func (s *MySQLQuotaRequests) GetQuotaRequest(ctx context.Context, requestID string) (QuotaRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return QuotaRequest{}, err
	}
	r, err := scanQuotaRequest(s.DB.QueryRowContext(ctx,
		`SELECT `+quotaRequestCols+` FROM shared_agent_quota_requests WHERE tenant_id = ? AND request_id = ?`,
		tnt, requestID).Scan, tnt)
	if errors.Is(err, sql.ErrNoRows) {
		return QuotaRequest{}, ErrQuotaRequestNotFound
	}
	return r, err
}

// ListQuotaRequests returns matching requests newest-first.
func (s *MySQLQuotaRequests) ListQuotaRequests(ctx context.Context, q QuotaRequestQuery) ([]QuotaRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	where := "tenant_id = ?"
	args := []any{tnt}
	if q.OwnerUserID != "" {
		where += " AND owner_user_id = ?"
		args = append(args, q.OwnerUserID)
	}
	if q.RequesterUserID != "" {
		where += " AND requester_user_id = ?"
		args = append(args, q.RequesterUserID)
	}
	if q.RoomID != "" {
		where += " AND room_id = ?"
		args = append(args, q.RoomID)
	}
	if q.Status != "" {
		where += " AND status = ?"
		args = append(args, string(q.Status))
	}
	args = append(args, quotaListLimit(q.Limit))
	out, err := queryAll(ctx, s.DB,
		`SELECT `+quotaRequestCols+` FROM shared_agent_quota_requests WHERE `+where+
			` ORDER BY created_at_ms DESC LIMIT ?`, func(r rowScanner) (QuotaRequest, error) {
			return scanQuotaRequest(r.Scan, tnt)
		}, args...)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []QuotaRequest{}
	}
	return out, nil
}

// DecideQuotaRequest moves a pending request owned by the decider to its
// decision in one conditional UPDATE; a miss is classified by reading back.
func (s *MySQLQuotaRequests) DecideQuotaRequest(ctx context.Context, d QuotaDecision) (QuotaRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return QuotaRequest{}, err
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE shared_agent_quota_requests
		 SET status = ?, granted_runs = ?, decided_at_ms = ?, decided_by = ?, pending_key = NULL
		 WHERE tenant_id = ? AND request_id = ? AND owner_user_id = ? AND status = 'pending'`,
		string(d.Status), nullInt(d.GrantedRuns), d.AtMs, d.OwnerUserID, tnt, d.RequestID, d.OwnerUserID)
	if err != nil {
		return QuotaRequest{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		r, err := s.GetQuotaRequest(ctx, d.RequestID)
		if err != nil {
			return QuotaRequest{}, err
		}
		if r.OwnerUserID != d.OwnerUserID {
			return QuotaRequest{}, ErrQuotaRequestNotFound
		}
		return QuotaRequest{}, ErrQuotaRequestDecided
	}
	return s.GetQuotaRequest(ctx, d.RequestID)
}

// ReopenQuotaRequest returns an approved request to pending (compensation for
// a failed grant raise). A concurrently opened duplicate wins: the reopen is
// then refused with ErrQuotaRequestPending.
func (s *MySQLQuotaRequests) ReopenQuotaRequest(ctx context.Context, requestID string) error {
	r, err := s.GetQuotaRequest(ctx, requestID)
	if err != nil {
		return err
	}
	if r.Status != QuotaRequestApproved {
		return ErrQuotaRequestDecided
	}
	_, err = s.DB.ExecContext(ctx,
		`UPDATE shared_agent_quota_requests
		 SET status = 'pending', granted_runs = NULL, decided_at_ms = 0, decided_by = '', pending_key = ?
		 WHERE tenant_id = ? AND request_id = ? AND status = 'approved'`,
		quotaPendingKey(r.TenantID, r), r.TenantID, requestID)
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == mysqlErrDupEntry {
		return ErrQuotaRequestPending
	}
	return err
}
