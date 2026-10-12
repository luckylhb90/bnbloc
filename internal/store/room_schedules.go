package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Scheduled prompts (N2). Request-path methods are tenant-scoped by the
// context's tenant. The scheduler methods (ClaimDueSchedules,
// CompleteScheduleRun) are cross-tenant: one scheduler serves every tenant,
// and each claimed row carries its own tenant for the run.

// ErrScheduleNotFound reports a schedule that does not exist in the room.
var ErrScheduleNotFound = errors.New("store: schedule not found")

// ScheduleRunResult records one run (or skip) of a schedule.
//
// ClaimToken is the scheduler's claim of the slot. When it still matches the
// row, the run also advances the schedule: NextRunAtMs (0 = never again) and
// Disable (auto-pause), and releases the claim. When it does not match — the
// schedule was edited, paused, or re-claimed meanwhile — or is empty (a manual
// "run now"), only the last-run fields are recorded: an edit always wins over
// an in-flight run.
type ScheduleRunResult struct {
	ScheduleID  string
	ClaimToken  string
	AtMs        int64
	Status      string
	Error       string
	Seq         int64
	NextRunAtMs int64
	Disable     bool
}

// RoomScheduleStore persists scheduled prompts.
type RoomScheduleStore interface {
	CreateSchedule(ctx context.Context, s wire.RoomSchedule) (wire.RoomSchedule, error)
	GetSchedule(ctx context.Context, roomID, scheduleID string) (wire.RoomSchedule, error)
	// ListSchedules returns the room's schedules, oldest first.
	ListSchedules(ctx context.Context, roomID string) ([]wire.RoomSchedule, error)
	// CountSchedules returns how many schedules the room has, and how many
	// of them createdBy owns.
	CountSchedules(ctx context.Context, roomID, createdBy string) (room, mine int, err error)
	// UpdateSchedule rewrites the mutable fields (channel, text, recipient,
	// recurrence, timezone, enabled, next run, last error/status, updated
	// time) and drops any in-flight claim.
	UpdateSchedule(ctx context.Context, s wire.RoomSchedule) error
	DeleteSchedule(ctx context.Context, roomID, scheduleID string) error
	// ClaimDueSchedules atomically claims up to limit enabled schedules whose
	// next run is due at nowMs and that no live claim holds, stamping them
	// with a fresh claim token (returned) until leaseUntilMs. Two concurrent
	// claimers never receive the same row. The returned rows carry the
	// claimed slot in NextRunAtMs.
	ClaimDueSchedules(ctx context.Context, nowMs, leaseUntilMs int64, limit int) (token string, rows []wire.RoomSchedule, err error)
	// CompleteScheduleRun records a run (see ScheduleRunResult).
	CompleteScheduleRun(ctx context.Context, r ScheduleRunResult) error
}

// --- memory ---

type memSchedule struct {
	s            wire.RoomSchedule
	claimToken   string
	claimedUntil int64
}

// MemoryRoomSchedules is the in-memory RoomScheduleStore (dev / tests).
type MemoryRoomSchedules struct {
	mu   sync.Mutex
	rows map[string]*memSchedule // scheduleID -> row
}

// NewMemoryRoomSchedules constructs an empty in-memory schedule store.
func NewMemoryRoomSchedules() *MemoryRoomSchedules {
	return &MemoryRoomSchedules{rows: map[string]*memSchedule{}}
}

var _ RoomScheduleStore = (*MemoryRoomSchedules)(nil)

func cloneSchedule(s wire.RoomSchedule) wire.RoomSchedule {
	s.Recurrence.Days = append([]int(nil), s.Recurrence.Days...)
	return s
}

func (m *MemoryRoomSchedules) CreateSchedule(ctx context.Context, s wire.RoomSchedule) (wire.RoomSchedule, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	s.TenantID = tid
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[s.ScheduleID] = &memSchedule{s: cloneSchedule(s)}
	return s, nil
}

// getLocked returns the tenant's schedule in the room. Caller holds mu.
func (m *MemoryRoomSchedules) getLocked(tid, roomID, scheduleID string) (*memSchedule, error) {
	r, ok := m.rows[scheduleID]
	if !ok || r.s.TenantID != tid || r.s.RoomID != roomID {
		return nil, ErrScheduleNotFound
	}
	return r, nil
}

func (m *MemoryRoomSchedules) GetSchedule(ctx context.Context, roomID, scheduleID string) (wire.RoomSchedule, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.getLocked(tid, roomID, scheduleID)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	return cloneSchedule(r.s), nil
}

func (m *MemoryRoomSchedules) ListSchedules(ctx context.Context, roomID string) ([]wire.RoomSchedule, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []wire.RoomSchedule{}
	for _, r := range m.rows {
		if r.s.TenantID == tid && r.s.RoomID == roomID {
			out = append(out, cloneSchedule(r.s))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs < out[j].CreatedAtMs
		}
		return out[i].ScheduleID < out[j].ScheduleID
	})
	return out, nil
}

func (m *MemoryRoomSchedules) CountSchedules(ctx context.Context, roomID, createdBy string) (int, int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	room, mine := 0, 0
	for _, r := range m.rows {
		if r.s.TenantID == tid && r.s.RoomID == roomID {
			room++
			if r.s.CreatedBy == createdBy {
				mine++
			}
		}
	}
	return room, mine, nil
}

func (m *MemoryRoomSchedules) UpdateSchedule(ctx context.Context, s wire.RoomSchedule) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.getLocked(tid, s.RoomID, s.ScheduleID)
	if err != nil {
		return err
	}
	cur := r.s
	cur.ChannelID, cur.Text, cur.Recipient, cur.Recurrence = s.ChannelID, s.Text, s.Recipient, s.Recurrence
	cur.Timezone, cur.Enabled, cur.NextRunAtMs = s.Timezone, s.Enabled, s.NextRunAtMs
	cur.LastStatus, cur.LastError, cur.UpdatedAtMs = s.LastStatus, s.LastError, s.UpdatedAtMs
	r.s = cloneSchedule(cur)
	r.claimToken, r.claimedUntil = "", 0
	return nil
}

func (m *MemoryRoomSchedules) DeleteSchedule(ctx context.Context, roomID, scheduleID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.getLocked(tid, roomID, scheduleID); err != nil {
		return err
	}
	delete(m.rows, scheduleID)
	return nil
}

func (m *MemoryRoomSchedules) ClaimDueSchedules(_ context.Context, nowMs, leaseUntilMs int64, limit int) (string, []wire.RoomSchedule, error) {
	token := newClaimToken()
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []*memSchedule
	for _, r := range m.rows {
		if r.s.Enabled && r.s.NextRunAtMs > 0 && r.s.NextRunAtMs <= nowMs && r.claimedUntil < nowMs {
			due = append(due, r)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].s.NextRunAtMs < due[j].s.NextRunAtMs })
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	out := make([]wire.RoomSchedule, 0, len(due))
	for _, r := range due {
		r.claimToken, r.claimedUntil = token, leaseUntilMs
		out = append(out, cloneSchedule(r.s))
	}
	return token, out, nil
}

func (m *MemoryRoomSchedules) CompleteScheduleRun(_ context.Context, res ScheduleRunResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[res.ScheduleID]
	if !ok {
		return nil // deleted while running: nothing to record
	}
	r.s.LastRunAtMs, r.s.LastStatus, r.s.LastError = res.AtMs, res.Status, truncateRunError(res.Error)
	if res.Status == wire.ScheduleRunOK {
		r.s.LastSeq = res.Seq
		r.s.RunCount++
	}
	if res.ClaimToken != "" && r.claimToken == res.ClaimToken {
		r.s.NextRunAtMs = res.NextRunAtMs
		if res.Disable {
			r.s.Enabled = false
		}
		r.claimToken, r.claimedUntil = "", 0
	}
	return nil
}

// truncateRunError bounds a recorded error to the last_error column.
func truncateRunError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 512 {
		return s
	}
	cut := 509
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// --- MySQL ---

// MySQLRoomSchedules is the MySQL-backed RoomScheduleStore.
type MySQLRoomSchedules struct{ DB *sql.DB }

// NewMySQLRoomSchedules constructs a MySQL schedule store.
func NewMySQLRoomSchedules(db *sql.DB) *MySQLRoomSchedules { return &MySQLRoomSchedules{DB: db} }

var _ RoomScheduleStore = (*MySQLRoomSchedules)(nil)

const scheduleCols = `schedule_id, tenant_id, room_id, channel_id, created_by, text, recipient_json, recurrence_json,
  timezone, enabled, next_run_at_ms, last_run_at_ms, last_status, last_error, last_seq, run_count, created_at_ms, updated_at_ms`

func scanSchedule(row interface{ Scan(...any) error }) (wire.RoomSchedule, error) {
	var s wire.RoomSchedule
	var recip, recur []byte
	var next, last sql.NullInt64
	if err := row.Scan(&s.ScheduleID, &s.TenantID, &s.RoomID, &s.ChannelID, &s.CreatedBy, &s.Text, &recip, &recur,
		&s.Timezone, &s.Enabled, &next, &last, &s.LastStatus, &s.LastError, &s.LastSeq, &s.RunCount,
		&s.CreatedAtMs, &s.UpdatedAtMs); err != nil {
		return wire.RoomSchedule{}, err
	}
	if err := json.Unmarshal(recip, &s.Recipient); err != nil {
		return wire.RoomSchedule{}, err
	}
	if err := json.Unmarshal(recur, &s.Recurrence); err != nil {
		return wire.RoomSchedule{}, err
	}
	s.NextRunAtMs, s.LastRunAtMs = next.Int64, last.Int64
	return s, nil
}

func nullableMs(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func (m *MySQLRoomSchedules) CreateSchedule(ctx context.Context, s wire.RoomSchedule) (wire.RoomSchedule, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	s.TenantID = tid
	recip, err := jsonArg(s.Recipient)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	recur, err := jsonArg(s.Recurrence)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO room_schedules (`+scheduleCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ScheduleID, tid, s.RoomID, s.ChannelID, s.CreatedBy, s.Text, recip, recur,
		s.Timezone, s.Enabled, nullableMs(s.NextRunAtMs), nullableMs(s.LastRunAtMs), s.LastStatus, s.LastError,
		s.LastSeq, s.RunCount, s.CreatedAtMs, s.UpdatedAtMs)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	return s, nil
}

func (m *MySQLRoomSchedules) GetSchedule(ctx context.Context, roomID, scheduleID string) (wire.RoomSchedule, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	s, err := scanSchedule(m.DB.QueryRowContext(ctx, `SELECT `+scheduleCols+` FROM room_schedules
		WHERE tenant_id = ? AND room_id = ? AND schedule_id = ?`, tid, roomID, scheduleID))
	if errors.Is(err, sql.ErrNoRows) {
		return wire.RoomSchedule{}, ErrScheduleNotFound
	}
	return s, err
}

func (m *MySQLRoomSchedules) ListSchedules(ctx context.Context, roomID string) ([]wire.RoomSchedule, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT `+scheduleCols+` FROM room_schedules
		WHERE tenant_id = ? AND room_id = ? ORDER BY created_at_ms, schedule_id`, tid, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []wire.RoomSchedule{}
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *MySQLRoomSchedules) CountSchedules(ctx context.Context, roomID, createdBy string) (int, int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, 0, err
	}
	var room, mine int
	err = m.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(created_by = ?), 0) FROM room_schedules
		WHERE tenant_id = ? AND room_id = ?`, createdBy, tid, roomID).Scan(&room, &mine)
	return room, mine, err
}

func (m *MySQLRoomSchedules) UpdateSchedule(ctx context.Context, s wire.RoomSchedule) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	recip, err := jsonArg(s.Recipient)
	if err != nil {
		return err
	}
	recur, err := jsonArg(s.Recurrence)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET channel_id = ?, text = ?, recipient_json = ?,
		recurrence_json = ?, timezone = ?, enabled = ?, next_run_at_ms = ?, last_status = ?, last_error = ?,
		updated_at_ms = ?, claim_token = NULL, claimed_until_ms = NULL
		WHERE tenant_id = ? AND room_id = ? AND schedule_id = ?`,
		s.ChannelID, s.Text, recip, recur, s.Timezone, s.Enabled, nullableMs(s.NextRunAtMs), s.LastStatus,
		truncateRunError(s.LastError), s.UpdatedAtMs, tid, s.RoomID, s.ScheduleID)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrScheduleNotFound)
}

func (m *MySQLRoomSchedules) DeleteSchedule(ctx context.Context, roomID, scheduleID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx, `DELETE FROM room_schedules WHERE tenant_id = ? AND room_id = ? AND schedule_id = ?`,
		tid, roomID, scheduleID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// ClaimDueSchedules is one atomic UPDATE (the row locks of a single statement
// serialize concurrent claimers: a row another claimer stamped first no
// longer satisfies the claim predicate), then a read of the rows carrying
// this call's fresh token.
func (m *MySQLRoomSchedules) ClaimDueSchedules(ctx context.Context, nowMs, leaseUntilMs int64, limit int) (string, []wire.RoomSchedule, error) {
	if limit <= 0 {
		limit = 100
	}
	token := newClaimToken()
	if _, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET claim_token = ?, claimed_until_ms = ?
		WHERE enabled = 1 AND next_run_at_ms IS NOT NULL AND next_run_at_ms <= ?
		  AND (claimed_until_ms IS NULL OR claimed_until_ms < ?)
		ORDER BY next_run_at_ms LIMIT ?`, token, leaseUntilMs, nowMs, nowMs, limit); err != nil {
		return "", nil, err
	}
	rows, err := m.DB.QueryContext(ctx, `SELECT `+scheduleCols+` FROM room_schedules
		WHERE claim_token = ? ORDER BY next_run_at_ms`, token)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	out := []wire.RoomSchedule{}
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return "", nil, err
		}
		out = append(out, s)
	}
	return token, out, rows.Err()
}

// newClaimToken mints a per-call claim token.
func newClaimToken() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "clm_" + hex.EncodeToString(b)
}

// CompleteScheduleRun is one UPDATE. MySQL evaluates single-table SET
// assignments left to right, so every claim-guarded assignment reads the
// claim_token BEFORE the final assignment releases it.
func (m *MySQLRoomSchedules) CompleteScheduleRun(ctx context.Context, r ScheduleRunResult) error {
	ok := r.Status == wire.ScheduleRunOK
	_, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET
		last_run_at_ms = ?, last_status = ?, last_error = ?,
		last_seq = IF(?, ?, last_seq), run_count = run_count + IF(?, 1, 0),
		next_run_at_ms = IF(claim_token <=> ?, ?, next_run_at_ms),
		enabled = IF(claim_token <=> ? AND ?, 0, enabled),
		claimed_until_ms = IF(claim_token <=> ?, NULL, claimed_until_ms),
		claim_token = IF(claim_token <=> ?, NULL, claim_token)
		WHERE schedule_id = ?`,
		r.AtMs, r.Status, truncateRunError(r.Error),
		ok, r.Seq, ok,
		claimArg(r.ClaimToken), nullableMs(r.NextRunAtMs),
		claimArg(r.ClaimToken), r.Disable,
		claimArg(r.ClaimToken),
		claimArg(r.ClaimToken),
		r.ScheduleID)
	return err
}

// claimArg maps an empty claim token to a value no row can hold (a NULL
// would match an unclaimed row under <=>).
func claimArg(token string) string {
	if token == "" {
		return "\x00none"
	}
	return token
}
