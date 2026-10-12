package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// content_publish_requests (content_publish_requests.go). pending_key holds
// the component id while a request is pending (NULL otherwise), so the
// unique key admits one pending request per component.

const contentRequestCols = `request_id, tenant_id, component_id, kind, name, version, requested_by, note, status, decided_by, decision_note, decided_at_ms, created_at_ms`

func scanContentRequest(row rowScanner) (ContentPublishRequest, error) {
	var r ContentPublishRequest
	var status string
	if err := row.Scan(&r.ID, &r.TenantID, &r.ComponentID, &r.Kind, &r.Name, &r.Version, &r.RequestedBy, &r.Note, &status,
		&r.DecidedBy, &r.DecisionNote, &r.DecidedAtMs, &r.CreatedAtMs); err != nil {
		return ContentPublishRequest{}, err
	}
	r.Status = ContentRequestStatus(status)
	return r, nil
}

func (s *MySQLContentComponents) CreateContentRequest(ctx context.Context, r ContentPublishRequest) (ContentPublishRequest, error) {
	// cross-tenant: content ids are global (platform content spans tenants);
	// callers decide visibility (extpipe.MemberVisibility).
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ContentPublishRequest{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE content_publish_requests SET status = ?, pending_key = NULL, decided_at_ms = ?
  WHERE pending_key = ?`, string(ContentRequestWithdrawn), r.CreatedAtMs, r.ComponentID); err != nil {
		return ContentPublishRequest{}, err
	}
	r.Status = ContentRequestPending
	_, err = tx.ExecContext(ctx, `INSERT INTO content_publish_requests (`+contentRequestCols+`, pending_key)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.TenantID, r.ComponentID, r.Kind, r.Name, r.Version, r.RequestedBy, r.Note, string(r.Status),
		r.DecidedBy, r.DecisionNote, r.DecidedAtMs, r.CreatedAtMs, r.ComponentID)
	if isDupEntry(err) {
		return ContentPublishRequest{}, ErrContentVersionConflict
	}
	if err != nil {
		return ContentPublishRequest{}, err
	}
	return r, tx.Commit()
}

func (s *MySQLContentComponents) GetContentRequest(ctx context.Context, id string) (ContentPublishRequest, error) {
	// cross-tenant: request ids are global; DecideContentRequest checks the
	// tenant, and the caller of a read compares it.
	r, err := scanContentRequest(s.DB.QueryRowContext(ctx, `SELECT `+contentRequestCols+` FROM content_publish_requests WHERE request_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ContentPublishRequest{}, ErrContentRequestNotFound
	}
	return r, err
}

func (s *MySQLContentComponents) ListContentRequests(ctx context.Context, q ContentRequestQuery) ([]ContentPublishRequest, error) {
	var where []string
	var args []any
	for _, f := range []struct{ col, val string }{
		{"tenant_id", q.TenantID}, {"component_id", q.ComponentID}, {"requested_by", q.RequestedBy}, {"status", string(q.Status)},
	} {
		if f.val != "" {
			where, args = append(where, f.col+" = ?"), append(args, f.val)
		}
	}
	query := `SELECT ` + contentRequestCols + ` FROM content_publish_requests`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at_ms DESC, request_id DESC LIMIT ?"
	return queryAll(ctx, s.DB, query, scanContentRequest, append(args, contentRequestLimit(q.Limit))...)
}

func (s *MySQLContentComponents) DecideContentRequest(ctx context.Context, d ContentRequestDecision) (ContentPublishRequest, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE content_publish_requests
  SET status = ?, pending_key = NULL, decided_by = ?, decision_note = ?, decided_at_ms = ?
  WHERE request_id = ? AND tenant_id = ? AND status = ?`,
		string(d.Status), d.By, d.Note, d.AtMs, d.RequestID, d.TenantID, string(ContentRequestPending))
	if err != nil {
		return ContentPublishRequest{}, err
	}
	r, err := s.GetContentRequest(ctx, d.RequestID)
	if err != nil {
		return ContentPublishRequest{}, err
	}
	if r.TenantID != d.TenantID {
		return ContentPublishRequest{}, ErrContentRequestNotFound
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return r, ErrContentRequestDecided
	}
	return r, nil
}

// ReopenContentRequest is one conditional UPDATE of an approved request; the
// pending key refuses it beside a newer pending request (duplicate key), and
// a miss is classified by reading the request back.
func (s *MySQLContentComponents) ReopenContentRequest(ctx context.Context, id string) error {
	// cross-tenant: request ids are global; the caller decided it in its tenant.
	res, err := s.DB.ExecContext(ctx, `UPDATE content_publish_requests
  SET status = ?, pending_key = component_id, decided_by = '', decision_note = '', decided_at_ms = 0
  WHERE request_id = ? AND status = ?`, string(ContentRequestPending), id, string(ContentRequestApproved))
	if err != nil {
		return dupAs(err, ErrContentRequestDecided)
	}
	if err := requireMatched(res, ErrContentRequestDecided); err != nil {
		if _, gerr := s.GetContentRequest(ctx, id); gerr != nil {
			return gerr
		}
		return err
	}
	return nil
}
