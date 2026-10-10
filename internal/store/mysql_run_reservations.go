package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// ReserveRun admits one run in ONE conditional UPDATE (liveness, run quota and
// cost allowance are its WHERE clause, so concurrent reservations can never
// overrun a ceiling) and writes the run's audit record in the same
// transaction (see RunReservationStore). A miss is classified by reading the
// grant back.
func (s *MySQLGrant) ReserveRun(ctx context.Context, r RunReservation) (Grant, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return Grant{}, err
	}
	// A miss whose re-read still admits the run means a concurrent writer
	// (e.g. a re-authorization resetting usage) changed the row between the
	// two statements; retry the atomic UPDATE rather than admit outside it.
	for attempt := 0; ; attempt++ {
		admitted, err := s.reserveOnce(ctx, tnt, r)
		if err != nil {
			return Grant{}, err
		}
		if admitted {
			return s.GetGrant(ctx, r.GrantID)
		}
		g, err := s.GetGrant(ctx, r.GrantID)
		if err != nil {
			return Grant{}, err
		}
		if err := g.admitReservation(r.AtMs); err != nil {
			return Grant{}, err
		}
		if attempt >= 2 {
			return Grant{}, errors.New("store: reserve run: grant changed concurrently")
		}
	}
}

// reserveOnce runs one reserve transaction; admitted=false when the grant's
// conditional UPDATE matched nothing (nothing is written then).
func (s *MySQLGrant) reserveOnce(ctx context.Context, tnt string, r RunReservation) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE shared_agent_grants SET runs_used = runs_used + 1
		 WHERE grant_id = ? AND tenant_id = ? AND revoked_at_ms IS NULL
		   AND (expires_at_ms IS NULL OR expires_at_ms > ?)
		   AND (run_quota IS NULL OR runs_used < run_quota)
		   AND (cost_allowance IS NULL OR cost_used < cost_allowance)`,
		r.GrantID, tnt, r.AtMs)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	rec := reservationAudit(tnt, r)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO shared_agent_audit
		 (tenant_id, room_id, grant_id, actor_user_id, owner_user_id, action, decision, model, channel_id, at_ms, reservation_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tnt, rec.RoomID, nullStr(rec.GrantID), rec.ActorUserID, rec.OwnerUserID, rec.Action,
		rec.Decision, nullStr(rec.Model), rec.ChannelID, rec.AtMs, rec.ReservationID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// SettleRun attaches the run's usage to its reservation record and adds the
// cost to the grant in one transaction (see RunReservationStore). The record
// UPDATE only matches an open, unexpired reservation, so a settle can never
// apply twice.
func (s *MySQLGrant) SettleRun(ctx context.Context, grantID, reservationID string, u RunUsage, nowMs int64) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`UPDATE shared_agent_audit
		 SET settled_at_ms = ?, model = COALESCE(?, model), cost = ?, input_tokens = ?, output_tokens = ?
		 WHERE tenant_id = ? AND reservation_id = ? AND grant_id = ? AND settled_at_ms IS NULL AND at_ms > ?`,
		nowMs, nullStr(strings.TrimSpace(u.Model)), nullFloat(u.Cost), nullInt64(u.InputTokens), nullInt64(u.OutputTokens),
		tnt, reservationID, grantID, nowMs-RunReservationTTLms)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var settled sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT settled_at_ms FROM shared_agent_audit WHERE tenant_id = ? AND reservation_id = ? AND grant_id = ?`,
			tnt, reservationID, grantID).Scan(&settled)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrReservationNotFound
		case err != nil:
			return err
		case settled.Valid:
			return ErrReservationSettled
		default:
			return ErrReservationExpired
		}
	}
	if u.Cost != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE shared_agent_grants SET cost_used = cost_used + ? WHERE grant_id = ? AND tenant_id = ?`,
			*u.Cost, grantID, tnt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
