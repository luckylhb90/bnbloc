package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Sends from a member's computer outside a turn
// (docs/prd/2026-10-25-member-sends.md): a component's request, queued
// for the member's own bridge, taken by its claim, served while held. A row
// lives minutes. Both bridge job stores implement BridgeSendStore: a send
// needs the bridge's liveness.

// BridgeSend is one queued or taken send.
type BridgeSend struct {
	SendID      string `json:"sendId"`
	TenantID    string `json:"tenantId"`
	OwnerUserID string `json:"ownerUserId"`
	Component   string `json:"component"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	CreatedAtMs int64  `json:"createdAtMs"`
	// TakenBy is the bridge that took it ("": queued).
	TakenBy   string `json:"takenBy,omitempty"`
	TakenAtMs int64  `json:"takenAtMs,omitempty"`
}

// Bounds of a send (component.MaxSendWait, MaxSendHeld): queued, it waits
// BridgeSendWaitMs for a bridge; taken, it is served BridgeSendHeldMs.
const (
	BridgeSendWaitMs = 2 * 60 * 1000
	BridgeSendHeldMs = 5 * 60 * 1000
	// MaxSendsPerClaim bounds the sends one claim takes.
	MaxSendsPerClaim = 8
)

var (
	// ErrNoLiveBridge: the member has no live bridge of their own.
	ErrNoLiveBridge = errors.New("store: no live bridge of the member")
	// ErrBridgeSendExists: the send id is taken (a grant used twice).
	ErrBridgeSendExists = errors.New("store: the send exists")
	// ErrBridgeSendNotFound: no send the owner's bridge holds by that id.
	ErrBridgeSendNotFound = errors.New("store: send not found")
)

// BridgeSendStore queues sends for members' own bridges.
type BridgeSendStore interface {
	// PutBridgeSend queues s in the ctx tenant when its owner has a live
	// bridge of their own (not one serving every owner):
	// ErrNoLiveBridge, ErrBridgeSendExists.
	PutBridgeSend(ctx context.Context, s BridgeSend, nowMs int64) error
	// TakeBridgeSends hands the owner's queued sends (any tenant, at most
	// MaxSendsPerClaim, oldest first, queued within BridgeSendWaitMs) to
	// bridgeID, and drops the owner's expired ones.
	TakeBridgeSends(ctx context.Context, ownerUserID, bridgeID string, nowMs int64) ([]BridgeSend, error)
	// HeldBridgeSend is a send of the owner's that a bridge took within
	// BridgeSendHeldMs (any tenant): ErrBridgeSendNotFound.
	HeldBridgeSend(ctx context.Context, sendID, ownerUserID string, nowMs int64) (BridgeSend, error)
}

var (
	_ BridgeSendStore = (*MemoryBridgeJobs)(nil)
	_ BridgeSendStore = (*MySQLBridgeJobs)(nil)
)

// sendExpired is the one expiry rule of a send at nowMs.
func sendExpired(s BridgeSend, nowMs int64) bool {
	if s.TakenBy == "" {
		return s.CreatedAtMs+BridgeSendWaitMs <= nowMs
	}
	return s.TakenAtMs+BridgeSendHeldMs <= nowMs
}

func (s *MemoryBridgeJobs) PutBridgeSend(ctx context.Context, send BridgeSend, nowMs int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	live := false
	for _, hb := range s.heartbeats {
		live = live || (hb.OwnerUserID == send.OwnerUserID && !hb.ServeAllOwners && BridgeLive(hb, nowMs))
	}
	if !live {
		return ErrNoLiveBridge
	}
	if s.sends == nil {
		s.sends = map[string]BridgeSend{}
	}
	if _, ok := s.sends[send.SendID]; ok {
		return ErrBridgeSendExists
	}
	send.TenantID, send.CreatedAtMs, send.TakenBy, send.TakenAtMs = tid, nowMs, "", 0
	s.sends[send.SendID] = send
	return nil
}

func (s *MemoryBridgeJobs) TakeBridgeSends(_ context.Context, ownerUserID, bridgeID string, nowMs int64) ([]BridgeSend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []BridgeSend
	for id, send := range s.sends {
		switch {
		case send.OwnerUserID != ownerUserID:
		case sendExpired(send, nowMs):
			delete(s.sends, id)
		case send.TakenBy == "":
			out = append(out, send)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs < out[j].CreatedAtMs
		}
		return out[i].SendID < out[j].SendID
	})
	if len(out) > MaxSendsPerClaim {
		out = out[:MaxSendsPerClaim]
	}
	for i := range out {
		out[i].TakenBy, out[i].TakenAtMs = bridgeID, nowMs
		s.sends[out[i].SendID] = out[i]
	}
	return out, nil
}

func (s *MemoryBridgeJobs) HeldBridgeSend(_ context.Context, sendID, ownerUserID string, nowMs int64) (BridgeSend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	send, ok := s.sends[sendID]
	if !ok || send.OwnerUserID != ownerUserID || send.TakenBy == "" || sendExpired(send, nowMs) {
		return BridgeSend{}, ErrBridgeSendNotFound
	}
	return send, nil
}

const bridgeSendCols = `send_id, tenant_id, owner_user_id, component, method, url, created_at_ms, taken_by, taken_at_ms`

func scanBridgeSend(r rowScanner) (BridgeSend, error) {
	var s BridgeSend
	err := r.Scan(&s.SendID, &s.TenantID, &s.OwnerUserID, &s.Component, &s.Method, &s.URL, &s.CreatedAtMs, &s.TakenBy, &s.TakenAtMs)
	return s, err
}

func (m *MySQLBridgeJobs) PutBridgeSend(ctx context.Context, send BridgeSend, nowMs int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	// cross-tenant: a bridge id is global; any live bridge of the member's
	// own may take the send (the claim is cross-tenant, as for jobs).
	var live int
	if err := m.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_heartbeats h WHERE h.owner_user_id = ? AND h.serve_all_owners = 0 AND `+liveBridgePred,
		send.OwnerUserID, bridgeLiveSince(nowMs)).Scan(&live); err != nil {
		return err
	}
	if live == 0 {
		return ErrNoLiveBridge
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO bridge_sends (`+bridgeSendCols+`) VALUES (?,?,?,?,?,?,?,'',0)`,
		send.SendID, tid, send.OwnerUserID, send.Component, send.Method, send.URL, nowMs)
	return dupAs(err, ErrBridgeSendExists)
}

func (m *MySQLBridgeJobs) TakeBridgeSends(ctx context.Context, ownerUserID, bridgeID string, nowMs int64) ([]BridgeSend, error) {
	// cross-tenant: the owner's bridge takes their sends from every tenant,
	// as it claims their jobs; each send carries its own tenant.
	if _, err := m.DB.ExecContext(ctx, `DELETE FROM bridge_sends WHERE owner_user_id = ? AND ((taken_by = '' AND created_at_ms <= ?) OR (taken_by <> '' AND taken_at_ms <= ?))`,
		ownerUserID, nowMs-BridgeSendWaitMs, nowMs-BridgeSendHeldMs); err != nil {
		return nil, err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+bridgeSendCols+` FROM bridge_sends WHERE owner_user_id = ? AND taken_by = '' ORDER BY created_at_ms, send_id LIMIT ? FOR UPDATE`,
		ownerUserID, MaxSendsPerClaim)
	if err != nil {
		return nil, err
	}
	var out []BridgeSend
	for rows.Next() {
		s, err := scanBridgeSend(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if _, err := tx.ExecContext(ctx, `UPDATE bridge_sends SET taken_by = ?, taken_at_ms = ? WHERE send_id = ?`, bridgeID, nowMs, out[i].SendID); err != nil {
			return nil, err
		}
		out[i].TakenBy, out[i].TakenAtMs = bridgeID, nowMs
	}
	return out, tx.Commit()
}

func (m *MySQLBridgeJobs) HeldBridgeSend(ctx context.Context, sendID, ownerUserID string, nowMs int64) (BridgeSend, error) {
	// cross-tenant: the send id is global and the owner authorizes it; the
	// send carries its own tenant.
	s, err := scanBridgeSend(m.DB.QueryRowContext(ctx, `SELECT `+bridgeSendCols+` FROM bridge_sends WHERE send_id = ? AND owner_user_id = ?`, sendID, ownerUserID))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (s.TakenBy == "" || sendExpired(s, nowMs))) {
		return BridgeSend{}, ErrBridgeSendNotFound
	}
	return s, err
}
