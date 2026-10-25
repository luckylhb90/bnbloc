package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
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

// ErrScheduleExists refuses a schedule id already taken (ids are global: the
// primary key carries no tenant).
var ErrScheduleExists = errors.New("store: schedule id already exists")

// MaxScheduleClaim bounds one ClaimDueSchedules call (and is its default).
const MaxScheduleClaim = 100

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
	// Summary is what an ai run did, in a line (replaces the last one).
	Summary string
}

// RoomScheduleStore persists scheduled prompts.
type RoomScheduleStore interface {
	// CreateSchedule inserts a schedule; ErrScheduleExists when the id is
	// taken in any tenant.
	CreateSchedule(ctx context.Context, s wire.RoomSchedule) (wire.RoomSchedule, error)
	GetSchedule(ctx context.Context, roomID, scheduleID string) (wire.RoomSchedule, error)
	// ListSchedules returns the room's schedules, oldest first (then id).
	ListSchedules(ctx context.Context, roomID string) ([]wire.RoomSchedule, error)
	// CountSchedules returns how many schedules the room has, and how many
	// of them createdBy owns.
	CountSchedules(ctx context.Context, roomID, createdBy string) (room, mine int, err error)
	// UpdateSchedule rewrites the mutable fields (channel, text, recipient,
	// recurrence, timezone, enabled, next run, last error/status, updated
	// time) and drops any in-flight claim.
	UpdateSchedule(ctx context.Context, s wire.RoomSchedule) error
	DeleteSchedule(ctx context.Context, roomID, scheduleID string) error
	// ClaimDueSchedules atomically claims up to clampLimit(limit,
	// MaxScheduleClaim, MaxScheduleClaim) enabled schedules whose next run is
	// due at nowMs and that no live claim holds (a claim is live strictly
	// before its lease end, liveAt), earliest due first (then id), stamping
	// them with a fresh claim token (returned) until leaseUntilMs. Two
	// concurrent claimers never receive the same row. The returned rows carry
	// the claimed slot in NextRunAtMs.
	ClaimDueSchedules(ctx context.Context, nowMs, leaseUntilMs int64, limit int) (token string, rows []wire.RoomSchedule, err error)
	// CompleteScheduleRun records a run (see ScheduleRunResult). A run
	// that still holds its claim also clears the platform event it
	// answered (wire.RoomSchedule.Event).
	CompleteScheduleRun(ctx context.Context, r ScheduleRunResult) error
	// ListEventSchedules returns every enabled schedule with an event
	// recurrence (wire.RecurEvent) that q reaches, oldest first (then id).
	// Cross-tenant: the dispatch of a platform event reads every tenant's,
	// and each row carries its own tenant.
	ListEventSchedules(ctx context.Context, q EventScheduleQuery) ([]wire.RoomSchedule, error)
	// TriggerEventSchedule sets the run of an enabled event schedule for
	// ev (cross-tenant, by its global id), coalesced so a burst of events
	// runs it at most once per minGapMs: a run already pending keeps its
	// time and takes ev (the latest event); a run in flight (a live
	// claim) loses its claim's hold on the schedule (its completion
	// records only the run) and is followed by one more at minGapMs after
	// its slot at the earliest; otherwise it runs at nowMs, or minGapMs
	// after its last run when that is later. It returns when the run is
	// due; ok is false when the schedule is gone, paused or no longer
	// follows events.
	TriggerEventSchedule(ctx context.Context, scheduleID string, ev wire.CloudEvent, nowMs, minGapMs int64) (runAtMs int64, ok bool, err error)
	// SetScheduleHook gives the tenant's schedule in the room a webhook:
	// hook (its id and time) with its secret sealed as sealedSecret. A new
	// id replaces a revoked or rotated one; its refusals start at zero.
	// ErrScheduleNotFound when there is no such schedule; ErrHookExists
	// when the id is another schedule's.
	SetScheduleHook(ctx context.Context, roomID, scheduleID string, hook wire.ScheduleHook, sealedSecret string) error
	// DeleteScheduleHook revokes the schedule's webhook (none is not an
	// error); ErrScheduleNotFound when there is no such schedule.
	DeleteScheduleHook(ctx context.Context, roomID, scheduleID string) error
	// ScheduleByHook returns the schedule whose webhook id is hookID and
	// its sealed secret (cross-tenant: a webhook request names only the
	// hook); ErrScheduleNotFound when no schedule has it.
	ScheduleByHook(ctx context.Context, hookID string) (wire.RoomSchedule, string, error)
	// CountHookRefused counts one refused request at hookID (cross-tenant;
	// a hook that is gone counts nothing).
	CountHookRefused(ctx context.Context, hookID string, atMs int64) error
	// CountEventDropped counts one event a rate limit dropped from the
	// schedule's trigger (cross-tenant). notice is true for the first drop
	// since the schedule was created or last edited (UpdateSchedule): the
	// room's owner is told once, not per drop.
	CountEventDropped(ctx context.Context, scheduleID string) (notice bool, err error)
}

// EventScheduleQuery is who an event reaches: the tenants (none: every
// tenant) and, for a room's own event, that room ("" : every room).
type EventScheduleQuery struct {
	Tenants []string
	RoomID  string
}

// reaches reports whether a schedule of tenantID in roomID is in q.
func (q EventScheduleQuery) reaches(tenantID, roomID string) bool {
	return (len(q.Tenants) == 0 || slices.Contains(q.Tenants, tenantID)) && (q.RoomID == "" || q.RoomID == roomID)
}

// ErrHookExists refuses a webhook id another schedule holds.
var ErrHookExists = errors.New("store: webhook id already exists")

// --- memory ---

type memSchedule struct {
	s            wire.RoomSchedule
	claimToken   string
	claimedUntil int64
	hookSecret   string
	dropNoticed  bool
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
	s.Recurrence.Types = append([]string(nil), s.Recurrence.Types...)
	if f := s.Recurrence.Filter; f != nil {
		cp := *f
		cp.Data = maps.Clone(f.Data)
		s.Recurrence.Filter = &cp
	}
	if s.Event != nil {
		ev := *s.Event
		ev.Data = slices.Clone(ev.Data)
		s.Event = &ev
	}
	if s.Hook != nil {
		h := *s.Hook
		s.Hook = &h
	}
	return s
}

func (m *MemoryRoomSchedules) CreateSchedule(ctx context.Context, s wire.RoomSchedule) (wire.RoomSchedule, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	// A new schedule has no webhook and no drops (SetScheduleHook,
	// CountEventDropped), as in MySQL.
	s.TenantID, s.Hook, s.DroppedEvents = tid, nil, 0
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.rows[s.ScheduleID]; taken {
		return wire.RoomSchedule{}, ErrScheduleExists
	}
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
	cur.LastStatus, cur.LastError, cur.UpdatedAtMs = s.LastStatus, truncateRunError(s.LastError), s.UpdatedAtMs
	r.s = cloneSchedule(cur)
	r.claimToken, r.claimedUntil, r.dropNoticed = "", 0, false
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
	limit = clampLimit(limit, MaxScheduleClaim, MaxScheduleClaim)
	m.mu.Lock()
	defer m.mu.Unlock()
	var due []*memSchedule
	for _, r := range m.rows {
		if r.s.Enabled && r.s.NextRunAtMs > 0 && r.s.NextRunAtMs <= nowMs && !liveAt(r.claimedUntil, nowMs) {
			due = append(due, r)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].s.NextRunAtMs != due[j].s.NextRunAtMs {
			return due[i].s.NextRunAtMs < due[j].s.NextRunAtMs
		}
		return due[i].s.ScheduleID < due[j].s.ScheduleID
	})
	if len(due) > limit {
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
	r.s.LastSummary = truncateRunError(res.Summary)
	if res.Status == wire.ScheduleRunError {
		r.s.FailedRuns++
	} else {
		r.s.FailedRuns = 0
	}
	if res.Status == wire.ScheduleRunOK {
		r.s.LastSeq = res.Seq
		r.s.RunCount++
	}
	if res.ClaimToken != "" && r.claimToken == res.ClaimToken {
		r.s.NextRunAtMs = res.NextRunAtMs
		if res.Disable {
			r.s.Enabled = false
		}
		r.s.Event = nil
		r.claimToken, r.claimedUntil = "", 0
	}
	return nil
}

func (m *MemoryRoomSchedules) ListEventSchedules(_ context.Context, q EventScheduleQuery) ([]wire.RoomSchedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []wire.RoomSchedule{}
	for _, r := range m.rows {
		if r.s.Enabled && r.s.Recurrence.Kind == wire.RecurEvent && q.reaches(r.s.TenantID, r.s.RoomID) {
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

func (m *MemoryRoomSchedules) TriggerEventSchedule(_ context.Context, scheduleID string, ev wire.CloudEvent, nowMs, minGapMs int64) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[scheduleID]
	if !ok || !r.s.Enabled || r.s.Recurrence.Kind != wire.RecurEvent {
		return 0, false, nil
	}
	switch {
	case liveAt(r.claimedUntil, nowMs):
		r.s.NextRunAtMs = max(nowMs, r.s.NextRunAtMs+minGapMs)
		r.claimToken, r.claimedUntil = "", 0
	case r.s.NextRunAtMs > 0:
		// A run already pending: it answers the latest event.
	case r.s.LastRunAtMs > 0:
		r.s.NextRunAtMs = max(nowMs, r.s.LastRunAtMs+minGapMs)
	default:
		r.s.NextRunAtMs = nowMs
	}
	r.s.Event = &ev
	r.s = cloneSchedule(r.s)
	return r.s.NextRunAtMs, true, nil
}

func (m *MemoryRoomSchedules) SetScheduleHook(ctx context.Context, roomID, scheduleID string, hook wire.ScheduleHook, sealedSecret string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.getLocked(tid, roomID, scheduleID)
	if err != nil {
		return err
	}
	for id, other := range m.rows {
		if id != scheduleID && other.s.Hook != nil && other.s.Hook.HookID == hook.HookID {
			return ErrHookExists
		}
	}
	h := wire.ScheduleHook{HookID: hook.HookID, CreatedAtMs: hook.CreatedAtMs}
	r.s.Hook, r.hookSecret = &h, sealedSecret
	return nil
}

func (m *MemoryRoomSchedules) DeleteScheduleHook(ctx context.Context, roomID, scheduleID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.getLocked(tid, roomID, scheduleID)
	if err != nil {
		return err
	}
	r.s.Hook, r.hookSecret = nil, ""
	return nil
}

// byHookLocked is the row whose webhook id is hookID. Caller holds mu.
func (m *MemoryRoomSchedules) byHookLocked(hookID string) *memSchedule {
	for _, r := range m.rows {
		if hookID != "" && r.s.Hook != nil && r.s.Hook.HookID == hookID {
			return r
		}
	}
	return nil
}

func (m *MemoryRoomSchedules) ScheduleByHook(_ context.Context, hookID string) (wire.RoomSchedule, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.byHookLocked(hookID)
	if r == nil {
		return wire.RoomSchedule{}, "", ErrScheduleNotFound
	}
	return cloneSchedule(r.s), r.hookSecret, nil
}

func (m *MemoryRoomSchedules) CountHookRefused(_ context.Context, hookID string, atMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.byHookLocked(hookID); r != nil {
		r.s.Hook.Refused++
		r.s.Hook.LastRefusedAtMs = atMs
	}
	return nil
}

func (m *MemoryRoomSchedules) CountEventDropped(_ context.Context, scheduleID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[scheduleID]
	if !ok {
		return false, nil
	}
	r.s.DroppedEvents++
	notice := !r.dropNoticed
	r.dropNoticed = true
	return notice, nil
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
type MySQLRoomSchedules struct{ DB *DB }

// NewMySQLRoomSchedules constructs a MySQL schedule store.
func NewMySQLRoomSchedules(db *DB) *MySQLRoomSchedules { return &MySQLRoomSchedules{DB: db} }

var _ RoomScheduleStore = (*MySQLRoomSchedules)(nil)

// scheduleInsertCols are the columns a new schedule is written with (a new
// schedule has no webhook and no drops).
const scheduleInsertCols = `schedule_id, tenant_id, room_id, channel_id, created_by, text, recipient_json, recurrence_json,
  timezone, enabled, next_run_at_ms, last_run_at_ms, last_status, last_error, last_summary, failed_runs, last_seq, run_count, created_at_ms, updated_at_ms,
  pending_event_json`

// scheduleCols are the columns a schedule is read with: never the
// webhook's secret (ScheduleByHook reads it).
const scheduleCols = scheduleInsertCols + `, hook_id, hook_created_at_ms, hook_refused, hook_refused_at_ms, events_dropped`

func scanSchedule(row rowScanner) (wire.RoomSchedule, error) {
	return scanScheduleWith(row)
}

// scanScheduleWith scans scheduleCols and then extra.
func scanScheduleWith(row rowScanner, extra ...any) (wire.RoomSchedule, error) {
	var s wire.RoomSchedule
	var recip, recur, event []byte
	var next, last, hookAt, refusedAt sql.NullInt64
	var hookID sql.NullString
	var refused int64
	dest := []any{&s.ScheduleID, &s.TenantID, &s.RoomID, &s.ChannelID, &s.CreatedBy, &s.Text, &recip, &recur,
		&s.Timezone, &s.Enabled, &next, &last, &s.LastStatus, &s.LastError, &s.LastSummary, &s.FailedRuns, &s.LastSeq, &s.RunCount,
		&s.CreatedAtMs, &s.UpdatedAtMs, &event, &hookID, &hookAt, &refused, &refusedAt, &s.DroppedEvents}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return wire.RoomSchedule{}, err
	}
	if len(event) > 0 && string(event) != "null" {
		s.Event = &wire.CloudEvent{}
		if err := json.Unmarshal(event, s.Event); err != nil {
			return wire.RoomSchedule{}, err
		}
		// A JSON column re-spaces what it stores: the event's data reads
		// back compact, as it was written (wire.ValidEvent) and as the
		// memory store holds it.
		var data bytes.Buffer
		if len(s.Event.Data) > 0 && json.Compact(&data, s.Event.Data) == nil {
			s.Event.Data = data.Bytes()
		}
	}
	if hookID.Valid && hookID.String != "" {
		s.Hook = &wire.ScheduleHook{HookID: hookID.String, CreatedAtMs: hookAt.Int64, Refused: refused, LastRefusedAtMs: refusedAt.Int64}
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
	event, err := pendingEventArg(s.Event)
	if err != nil {
		return wire.RoomSchedule{}, err
	}
	s.Hook, s.DroppedEvents = nil, 0
	_, err = m.DB.ExecContext(ctx, `INSERT INTO room_schedules (`+scheduleInsertCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ScheduleID, tid, s.RoomID, s.ChannelID, s.CreatedBy, s.Text, recip, recur,
		s.Timezone, s.Enabled, nullIfZero(s.NextRunAtMs), nullIfZero(s.LastRunAtMs), s.LastStatus, s.LastError, truncateRunError(s.LastSummary), s.FailedRuns,
		s.LastSeq, s.RunCount, s.CreatedAtMs, s.UpdatedAtMs, event)
	if err != nil {
		return wire.RoomSchedule{}, dupAs(err, ErrScheduleExists)
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
	return queryAll(ctx, m.DB, `SELECT `+scheduleCols+` FROM room_schedules
		WHERE tenant_id = ? AND room_id = ? ORDER BY created_at_ms, schedule_id`, scanSchedule, tid, roomID)
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
		updated_at_ms = ?, claim_token = NULL, claimed_until_ms = NULL, drop_noticed = 0
		WHERE tenant_id = ? AND room_id = ? AND schedule_id = ?`,
		s.ChannelID, s.Text, recip, recur, s.Timezone, s.Enabled, nullIfZero(s.NextRunAtMs), s.LastStatus,
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
	// cross-tenant: one scheduler claims every tenant's due schedules; each
	// row carries its own tenant for the run.
	token := newClaimToken()
	// A claim is live while claimed_until_ms > now (liveAt), so it is
	// claimable at claimed_until_ms <= now.
	if _, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET claim_token = ?, claimed_until_ms = ?
		WHERE enabled = 1 AND next_run_at_ms IS NOT NULL AND next_run_at_ms <= ?
		  AND (claimed_until_ms IS NULL OR claimed_until_ms <= ?)
		ORDER BY next_run_at_ms, schedule_id LIMIT ?`, token, leaseUntilMs, nowMs, nowMs, clampLimit(limit, MaxScheduleClaim, MaxScheduleClaim)); err != nil {
		return "", nil, err
	}
	out, err := queryAll(ctx, m.DB, `SELECT `+scheduleCols+` FROM room_schedules
		WHERE claim_token = ? ORDER BY next_run_at_ms, schedule_id`, scanSchedule, token)
	if err != nil {
		return "", nil, err
	}
	return token, out, nil
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
	// cross-tenant: the scheduler records the run of a row it claimed; the
	// schedule id is global.
	ok := r.Status == wire.ScheduleRunOK
	_, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET
		last_run_at_ms = ?, last_status = ?, last_error = ?, last_summary = ?,
		failed_runs = IF(?, failed_runs + 1, 0),
		last_seq = IF(?, ?, last_seq), run_count = run_count + IF(?, 1, 0),
		next_run_at_ms = IF(claim_token <=> ?, ?, next_run_at_ms),
		enabled = IF(claim_token <=> ? AND ?, 0, enabled),
		pending_event_json = IF(claim_token <=> ?, NULL, pending_event_json),
		claimed_until_ms = IF(claim_token <=> ?, NULL, claimed_until_ms),
		claim_token = IF(claim_token <=> ?, NULL, claim_token)
		WHERE schedule_id = ?`,
		r.AtMs, r.Status, truncateRunError(r.Error), truncateRunError(r.Summary),
		r.Status == wire.ScheduleRunError,
		ok, r.Seq, ok,
		claimArg(r.ClaimToken), nullIfZero(r.NextRunAtMs),
		claimArg(r.ClaimToken), r.Disable,
		claimArg(r.ClaimToken),
		claimArg(r.ClaimToken),
		claimArg(r.ClaimToken),
		r.ScheduleID)
	return err
}

// pendingEventArg is a schedule's pending event as its column holds it
// (NULL for none).
func pendingEventArg(ev *wire.CloudEvent) (any, error) {
	if ev == nil {
		return nil, nil
	}
	return jsonArg(ev)
}

// eventKindSQL selects the schedules with an event recurrence.
const eventKindSQL = `JSON_UNQUOTE(JSON_EXTRACT(recurrence_json, '$.kind')) = '` + wire.RecurEvent + `'`

func (m *MySQLRoomSchedules) ListEventSchedules(ctx context.Context, eq EventScheduleQuery) ([]wire.RoomSchedule, error) {
	// cross-tenant: a platform event reaches the schedules of every tenant
	// it names (or of all); each row carries its own tenant.
	q := `SELECT ` + scheduleCols + ` FROM room_schedules WHERE enabled = 1 AND ` + eventKindSQL
	args := []any{}
	if len(eq.Tenants) > 0 {
		q += ` AND tenant_id IN (?` + strings.Repeat(",?", len(eq.Tenants)-1) + `)`
		for _, t := range eq.Tenants {
			args = append(args, t)
		}
	}
	if eq.RoomID != "" {
		q += ` AND room_id = ?`
		args = append(args, eq.RoomID)
	}
	return queryAll(ctx, m.DB, q+` ORDER BY created_at_ms, schedule_id`, scanSchedule, args...)
}

func (m *MySQLRoomSchedules) SetScheduleHook(ctx context.Context, roomID, scheduleID string, hook wire.ScheduleHook, sealedSecret string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET hook_id = ?, hook_secret = ?, hook_created_at_ms = ?,
		hook_refused = 0, hook_refused_at_ms = NULL
		WHERE tenant_id = ? AND room_id = ? AND schedule_id = ?`,
		hook.HookID, sealedSecret, hook.CreatedAtMs, tid, roomID, scheduleID)
	if err != nil {
		return dupAs(err, ErrHookExists)
	}
	return requireMatched(res, ErrScheduleNotFound)
}

func (m *MySQLRoomSchedules) DeleteScheduleHook(ctx context.Context, roomID, scheduleID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET hook_id = NULL, hook_secret = NULL, hook_created_at_ms = NULL,
		hook_refused = 0, hook_refused_at_ms = NULL
		WHERE tenant_id = ? AND room_id = ? AND schedule_id = ?`, tid, roomID, scheduleID)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrScheduleNotFound)
}

func (m *MySQLRoomSchedules) ScheduleByHook(ctx context.Context, hookID string) (wire.RoomSchedule, string, error) {
	// cross-tenant: a webhook request names only its hook; the row
	// carries its tenant.
	var secret sql.NullString
	s, err := scanScheduleWith(m.DB.QueryRowContext(ctx, `SELECT `+scheduleCols+`, hook_secret FROM room_schedules
		WHERE hook_id = ?`, hookID), &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return wire.RoomSchedule{}, "", ErrScheduleNotFound
	}
	return s, secret.String, err
}

func (m *MySQLRoomSchedules) CountHookRefused(ctx context.Context, hookID string, atMs int64) error {
	// cross-tenant: the refused request named only the hook.
	_, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET hook_refused = hook_refused + 1, hook_refused_at_ms = ?
		WHERE hook_id = ?`, atMs, hookID)
	return err
}

// CountEventDropped is one UPDATE that sets the notice flag and carries its
// previous value out as the statement's insert id (LAST_INSERT_ID(expr)),
// so two concurrent drops never both notice and no second read races.
func (m *MySQLRoomSchedules) CountEventDropped(ctx context.Context, scheduleID string) (bool, error) {
	// cross-tenant: the dispatch addresses a schedule by its global id.
	res, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET events_dropped = events_dropped + 1,
		drop_noticed = LAST_INSERT_ID(drop_noticed) * 0 + 1
		WHERE schedule_id = ?`, scheduleID)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	prev, err := res.LastInsertId()
	return err == nil && prev == 0, err
}

// TriggerEventSchedule is one UPDATE (its row lock serializes it with a
// claim and a completion). MySQL evaluates single-table SET assignments
// left to right: next_run_at_ms and claim_token read the claim before
// claimed_until_ms releases it.
func (m *MySQLRoomSchedules) TriggerEventSchedule(ctx context.Context, scheduleID string, ev wire.CloudEvent, nowMs, minGapMs int64) (int64, bool, error) {
	// cross-tenant: the dispatch addresses a schedule by its global id.
	event, err := jsonArg(ev)
	if err != nil {
		return 0, false, err
	}
	if _, err := m.DB.ExecContext(ctx, `UPDATE room_schedules SET
		pending_event_json = ?,
		next_run_at_ms = CASE
			WHEN claimed_until_ms > ? THEN GREATEST(?, next_run_at_ms + ?)
			WHEN next_run_at_ms IS NOT NULL THEN next_run_at_ms
			WHEN last_run_at_ms IS NOT NULL THEN GREATEST(?, last_run_at_ms + ?)
			ELSE ? END,
		claim_token = IF(claimed_until_ms > ?, NULL, claim_token),
		claimed_until_ms = IF(claimed_until_ms > ?, NULL, claimed_until_ms)
		WHERE schedule_id = ? AND enabled = 1 AND `+eventKindSQL,
		event, nowMs, nowMs, minGapMs, nowMs, minGapMs, nowMs, nowMs, nowMs, scheduleID); err != nil {
		return 0, false, err
	}
	var at sql.NullInt64
	err = m.DB.QueryRowContext(ctx, `SELECT next_run_at_ms FROM room_schedules
		WHERE schedule_id = ? AND enabled = 1 AND `+eventKindSQL, scheduleID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return at.Int64, err == nil && at.Valid, err
}

// claimArg maps an empty claim token to a value no row can hold (a NULL
// would match an unclaimed row under <=>).
func claimArg(token string) string {
	if token == "" {
		return "\x00none"
	}
	return token
}
