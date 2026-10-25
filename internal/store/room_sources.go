package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room sources (docs/prd/2026-10-20-rooms-as-components.md §2.3): a room's
// ask to read a kind of organization record (wire.RoomSources), recorded
// when a room package asking for it is installed, and the organization
// admin's answer (a scope pinned to a person, or none); and the rooms whose
// owner opted their own records in to grants of scope opted-in. The rule of
// what a grant covers, and when it goes back to waiting, is the gateway's
// (room_sources.go); this store keeps the facts. Both belong to their room
// and are deleted with it.

// RoomSourceGrant is one room's ask for a source and its answer. Scope ""
// is waiting (never granted, withdrawn or paused). AliasKey is random per
// ask: the key of the opaque alias a reader who may not see names reads a
// person as, stable for the ask's life. Grantee is the person a grant is
// pinned to (the reading room's owner when the admin granted it; only
// their schedules read it) and Watch what the admins consented to and
// were told since; both are empty while the ask waits.
type RoomSourceGrant struct {
	TenantID         string
	RoomID           string
	Source           string
	ComponentID      string
	ComponentName    string
	ComponentVersion string
	Purpose          string
	AliasKey         string
	Scope            string
	AskedBy          string
	AskedAtMs        int64
	DecidedBy        string
	DecidedAtMs      int64
	Grantee          string
	Watch            RoomSourceWatch
}

// RoomSourceWatch is what a grant's admins know of the reading room: its
// human members when granted (and each one they were told of since), the
// grantee's AI tasks as consented (at the grant, or an admin's last
// acknowledgment) and the tasks changed since, each with the explanation
// the admins were sent, until an admin acknowledges them.
type RoomSourceWatch struct {
	Members []string               `json:"members,omitempty"`
	Tasks   []wire.RoomSourceTask  `json:"tasks,omitempty"`
	Drift   []wire.RoomSourceDrift `json:"drift,omitempty"`
}

// RoomSourceDecision is a decision on an ask: Scope "" sends it back to
// waiting (a withdrawal or a pause) and empties Grantee and Watch.
type RoomSourceDecision struct {
	RoomID  string
	Source  string
	Scope   string
	Grantee string
	Watch   RoomSourceWatch
	By      string
	AtMs    int64
}

// RoomSourceOptIn is a room whose owner opted its records in to a
// source's grants of scope opted-in.
type RoomSourceOptIn struct {
	TenantID  string
	RoomID    string
	Source    string
	OptedBy   string
	OptedAtMs int64
}

// Room source errors.
var (
	ErrRoomSourceNotFound = errors.New("the room does not ask for this source")
	// ErrRoomSourceAsked: the room already asks for the source.
	ErrRoomSourceAsked = errors.New("the room already asks for this source")
)

// MaxRoomSourceGrantsListed bounds one organization's listing of asks.
const MaxRoomSourceGrantsListed = 500

// RoomSourceStore holds rooms' asks for sources, their grants and the
// rooms' opt-ins, in the context's tenant.
type RoomSourceStore interface {
	// AskRoomSource records a room's ask, waiting (its Scope, decision,
	// Grantee and Watch are ignored); ErrRoomSourceAsked when the room
	// already asks.
	AskRoomSource(ctx context.Context, g RoomSourceGrant) error
	// RoomSourceGrants lists the tenant's asks, newest first, at most
	// MaxRoomSourceGrantsListed.
	RoomSourceGrants(ctx context.Context) ([]RoomSourceGrant, error)
	// GetRoomSourceGrant returns a room's ask for source;
	// ErrRoomSourceNotFound when it asks for none.
	GetRoomSourceGrant(ctx context.Context, roomID, source string) (RoomSourceGrant, error)
	// DecideRoomSource sets an ask's scope ("" back to waiting), grantee
	// and watch, by whom and when, and returns it; ErrRoomSourceNotFound
	// when there is no ask.
	DecideRoomSource(ctx context.Context, d RoomSourceDecision) (RoomSourceGrant, error)
	// SetRoomSourceWatch replaces a grant's watch while it is still granted
	// to grantee (else nothing changes: the ask was decided again since).
	SetRoomSourceWatch(ctx context.Context, roomID, source, grantee string, w RoomSourceWatch) error
	// SetRoomSourceOptIn opts o's room in to source's opted-in grants (in)
	// or out; either is a no-op when already so.
	SetRoomSourceOptIn(ctx context.Context, o RoomSourceOptIn, in bool) error
	// RoomSourceOptIns lists the tenant's rooms opted in to source.
	RoomSourceOptIns(ctx context.Context, source string) ([]RoomSourceOptIn, error)
}

var (
	_ RoomSourceStore = (*MemoryRoomSources)(nil)
	_ RoomSourceStore = (*MySQLRoomSources)(nil)
)

// sortGrantsNewestFirst orders asks newest first (then room id): the
// listing's order on both backends.
func sortGrantsNewestFirst(gs []RoomSourceGrant) {
	sort.Slice(gs, func(i, k int) bool {
		if gs[i].AskedAtMs != gs[k].AskedAtMs {
			return gs[i].AskedAtMs > gs[k].AskedAtMs
		}
		return gs[i].RoomID < gs[k].RoomID
	})
}

// decisionOf is d as both backends store it: back to waiting keeps no
// grantee and no watch.
func decisionOf(d RoomSourceDecision) RoomSourceDecision {
	if d.Scope == "" {
		d.Grantee, d.Watch = "", RoomSourceWatch{}
	}
	return d
}

// --- memory ---

// MemoryRoomSources is the in-memory RoomSourceStore.
type MemoryRoomSources struct {
	mu     sync.Mutex
	grants map[[3]string]RoomSourceGrant // tenant, room, source
	optIns map[[3]string]RoomSourceOptIn
}

// NewMemoryRoomSources builds an empty store.
func NewMemoryRoomSources() *MemoryRoomSources {
	return &MemoryRoomSources{grants: map[[3]string]RoomSourceGrant{}, optIns: map[[3]string]RoomSourceOptIn{}}
}

// cloneWatch copies w's lists: the memory store hands out no aliases of
// what it keeps.
func cloneWatch(w RoomSourceWatch) RoomSourceWatch {
	return RoomSourceWatch{Members: slices.Clone(w.Members), Tasks: slices.Clone(w.Tasks), Drift: slices.Clone(w.Drift)}
}

func (m *MemoryRoomSources) AskRoomSource(ctx context.Context, g RoomSourceGrant) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{tid, g.RoomID, g.Source}
	if _, ok := m.grants[k]; ok {
		return ErrRoomSourceAsked
	}
	g.TenantID, g.Scope, g.DecidedBy, g.DecidedAtMs, g.Grantee, g.Watch = tid, "", "", 0, "", RoomSourceWatch{}
	m.grants[k] = g
	return nil
}

func (m *MemoryRoomSources) RoomSourceGrants(ctx context.Context) ([]RoomSourceGrant, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []RoomSourceGrant{}
	for k, g := range m.grants {
		if k[0] == tid {
			g.Watch = cloneWatch(g.Watch)
			out = append(out, g)
		}
	}
	sortGrantsNewestFirst(out)
	if len(out) > MaxRoomSourceGrantsListed {
		out = out[:MaxRoomSourceGrantsListed]
	}
	return out, nil
}

func (m *MemoryRoomSources) GetRoomSourceGrant(ctx context.Context, roomID, source string) (RoomSourceGrant, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomSourceGrant{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[[3]string{tid, roomID, source}]
	if !ok {
		return RoomSourceGrant{}, ErrRoomSourceNotFound
	}
	g.Watch = cloneWatch(g.Watch)
	return g, nil
}

func (m *MemoryRoomSources) DecideRoomSource(ctx context.Context, d RoomSourceDecision) (RoomSourceGrant, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomSourceGrant{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{tid, d.RoomID, d.Source}
	g, ok := m.grants[k]
	if !ok {
		return RoomSourceGrant{}, ErrRoomSourceNotFound
	}
	d = decisionOf(d)
	g.Scope, g.Grantee, g.Watch, g.DecidedBy, g.DecidedAtMs = d.Scope, d.Grantee, cloneWatch(d.Watch), d.By, d.AtMs
	m.grants[k] = g
	g.Watch = cloneWatch(g.Watch)
	return g, nil
}

func (m *MemoryRoomSources) SetRoomSourceWatch(ctx context.Context, roomID, source, grantee string, w RoomSourceWatch) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{tid, roomID, source}
	if g, ok := m.grants[k]; ok && g.Scope != "" && g.Grantee == grantee {
		g.Watch = cloneWatch(w)
		m.grants[k] = g
	}
	return nil
}

func (m *MemoryRoomSources) SetRoomSourceOptIn(ctx context.Context, o RoomSourceOptIn, in bool) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{tid, o.RoomID, o.Source}
	if !in {
		delete(m.optIns, k)
		return nil
	}
	if _, ok := m.optIns[k]; !ok {
		o.TenantID = tid
		m.optIns[k] = o
	}
	return nil
}

func (m *MemoryRoomSources) RoomSourceOptIns(ctx context.Context, source string) ([]RoomSourceOptIn, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []RoomSourceOptIn{}
	for k, o := range m.optIns {
		if k[0] == tid && k[2] == source {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].RoomID < out[k].RoomID })
	return out, nil
}

// room_source_grants, room_source_opt_ins: deleted with the room.
func (m *MemoryRoomSources) deleteRoomRows(tenantID, roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.grants {
		if k[0] == tenantID && k[1] == roomID {
			delete(m.grants, k)
		}
	}
	for k := range m.optIns {
		if k[0] == tenantID && k[1] == roomID {
			delete(m.optIns, k)
		}
	}
}

// --- MySQL ---

// MySQLRoomSources is the MySQL RoomSourceStore over room_source_grants
// and room_source_opt_ins.
type MySQLRoomSources struct{ DB *DB }

// NewMySQLRoomSources builds the MySQL store.
func NewMySQLRoomSources(db *DB) *MySQLRoomSources { return &MySQLRoomSources{DB: db} }

const roomSourceGrantCols = `tenant_id, room_id, source_name, component_id, component_name, component_version, purpose, alias_key,
  scope, asked_by, asked_at_ms, decided_by, decided_at_ms, grantee, COALESCE(watch_json, '')`

func scanRoomSourceGrant(r rowScanner) (RoomSourceGrant, error) {
	var g RoomSourceGrant
	var watch string
	err := r.Scan(&g.TenantID, &g.RoomID, &g.Source, &g.ComponentID, &g.ComponentName, &g.ComponentVersion, &g.Purpose, &g.AliasKey,
		&g.Scope, &g.AskedBy, &g.AskedAtMs, &g.DecidedBy, &g.DecidedAtMs, &g.Grantee, &watch)
	if err == nil && watch != "" {
		err = json.Unmarshal([]byte(watch), &g.Watch)
	}
	return g, err
}

// watchJSON is w as watch_json holds it (NULL when it holds nothing).
func watchJSON(w RoomSourceWatch) (any, error) {
	if len(w.Members) == 0 && len(w.Tasks) == 0 && len(w.Drift) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func (s *MySQLRoomSources) AskRoomSource(ctx context.Context, g RoomSourceGrant) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO room_source_grants (tenant_id, room_id, source_name, component_id, component_name,
  component_version, purpose, alias_key, scope, asked_by, asked_at_ms, decided_by, decided_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, '', 0)`,
		tid, g.RoomID, g.Source, g.ComponentID, g.ComponentName, g.ComponentVersion, g.Purpose, g.AliasKey, g.AskedBy, g.AskedAtMs)
	return dupAs(err, ErrRoomSourceAsked)
}

func (s *MySQLRoomSources) RoomSourceGrants(ctx context.Context) ([]RoomSourceGrant, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return queryAll(ctx, s.DB, `SELECT `+roomSourceGrantCols+` FROM room_source_grants WHERE tenant_id = ?
  ORDER BY asked_at_ms DESC, room_id LIMIT ?`, scanRoomSourceGrant, tid, MaxRoomSourceGrantsListed)
}

func (s *MySQLRoomSources) GetRoomSourceGrant(ctx context.Context, roomID, source string) (RoomSourceGrant, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomSourceGrant{}, err
	}
	g, err := scanRoomSourceGrant(s.DB.QueryRowContext(ctx, `SELECT `+roomSourceGrantCols+` FROM room_source_grants
  WHERE tenant_id = ? AND room_id = ? AND source_name = ?`, tid, roomID, source))
	if errors.Is(err, sql.ErrNoRows) {
		return RoomSourceGrant{}, ErrRoomSourceNotFound
	}
	return g, err
}

func (s *MySQLRoomSources) DecideRoomSource(ctx context.Context, d RoomSourceDecision) (RoomSourceGrant, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomSourceGrant{}, err
	}
	d = decisionOf(d)
	watch, err := watchJSON(d.Watch)
	if err != nil {
		return RoomSourceGrant{}, err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE room_source_grants SET scope = ?, grantee = ?, watch_json = ?, decided_by = ?, decided_at_ms = ?
  WHERE tenant_id = ? AND room_id = ? AND source_name = ?`, d.Scope, d.Grantee, watch, d.By, d.AtMs, tid, d.RoomID, d.Source)
	if err != nil {
		return RoomSourceGrant{}, err
	}
	if err := requireMatched(res, ErrRoomSourceNotFound); err != nil {
		return RoomSourceGrant{}, err
	}
	return s.GetRoomSourceGrant(ctx, d.RoomID, d.Source)
}

func (s *MySQLRoomSources) SetRoomSourceWatch(ctx context.Context, roomID, source, grantee string, w RoomSourceWatch) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	watch, err := watchJSON(w)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE room_source_grants SET watch_json = ?
  WHERE tenant_id = ? AND room_id = ? AND source_name = ? AND scope <> '' AND grantee = ?`, watch, tid, roomID, source, grantee)
	return err
}

func (s *MySQLRoomSources) SetRoomSourceOptIn(ctx context.Context, o RoomSourceOptIn, in bool) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	if !in {
		_, err = s.DB.ExecContext(ctx, `DELETE FROM room_source_opt_ins WHERE tenant_id = ? AND room_id = ? AND source_name = ?`, tid, o.RoomID, o.Source)
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT IGNORE INTO room_source_opt_ins (tenant_id, room_id, source_name, opted_by, opted_at_ms) VALUES (?, ?, ?, ?, ?)`,
		tid, o.RoomID, o.Source, o.OptedBy, o.OptedAtMs)
	return err
}

func (s *MySQLRoomSources) RoomSourceOptIns(ctx context.Context, source string) ([]RoomSourceOptIn, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return queryAll(ctx, s.DB, `SELECT tenant_id, room_id, source_name, opted_by, opted_at_ms FROM room_source_opt_ins
  WHERE tenant_id = ? AND source_name = ? ORDER BY room_id`, func(r rowScanner) (RoomSourceOptIn, error) {
		var o RoomSourceOptIn
		err := r.Scan(&o.TenantID, &o.RoomID, &o.Source, &o.OptedBy, &o.OptedAtMs)
		return o, err
	}, tid, source)
}
