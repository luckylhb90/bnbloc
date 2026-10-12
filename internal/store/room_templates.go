package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room templates (N3): tenant-scoped, author-owned room setups. A template
// is visible to the viewer when the viewer authored it or it is shared with
// the tenant.

// ErrTemplateNotFound reports a template that does not exist or that the
// viewer cannot see (another member's private template reads as missing).
var ErrTemplateNotFound = errors.New("store: room template not found")

// ErrTemplateForbidden reports a visible template the requester may not change.
var ErrTemplateForbidden = errors.New("store: room template not owned by requester")

// ErrTemplateExists refuses a template id already taken (ids are global: the
// primary key carries no tenant).
var ErrTemplateExists = errors.New("store: room template id already exists")

// MaxTemplateList bounds ListTemplates on both backends: the newest
// MaxTemplateList visible templates (there is no cursor; a tenant's
// template library is expected to stay well below it).
const MaxTemplateList = 200

// TemplatePatch is a partial template update; nil fields are unchanged.
type TemplatePatch struct {
	Name        *string
	Description *string
	Visibility  *string
	Body        *wire.RoomTemplateBody
}

// RoomTemplateStore persists room templates. Every method is tenant-scoped
// by the context's tenant.
type RoomTemplateStore interface {
	// CreateTemplate inserts a template; ErrTemplateExists when the id is
	// taken in any tenant.
	CreateTemplate(ctx context.Context, t wire.RoomTemplate) (wire.RoomTemplate, error)
	// ListTemplates returns templates visible to viewer (only viewer's own
	// when mineOnly), most recently updated first (then template id), at
	// most MaxTemplateList.
	ListTemplates(ctx context.Context, viewer string, mineOnly bool) ([]wire.RoomTemplate, error)
	// GetTemplate returns a template visible to viewer.
	GetTemplate(ctx context.Context, templateID, viewer string) (wire.RoomTemplate, error)
	// UpdateTemplate applies patch when requester authored the template.
	UpdateTemplate(ctx context.Context, templateID, requester string, patch TemplatePatch, atMs int64) (wire.RoomTemplate, error)
	// DeleteTemplate removes a template requester authored, or — when
	// moderator — any tenant-visible template.
	DeleteTemplate(ctx context.Context, templateID, requester string, moderator bool) error
	// UseTemplate increments the use counter of a template visible to viewer.
	UseTemplate(ctx context.Context, templateID, viewer string) error
}

func templateVisible(t wire.RoomTemplate, viewer string) bool {
	return t.CreatedBy == viewer || t.Visibility == wire.TemplateTenant
}

func applyTemplatePatch(t wire.RoomTemplate, p TemplatePatch, atMs int64) wire.RoomTemplate {
	if p.Name != nil {
		t.Name = *p.Name
	}
	if p.Description != nil {
		t.Description = *p.Description
	}
	if p.Visibility != nil {
		t.Visibility = *p.Visibility
	}
	if p.Body != nil {
		t.Body = *p.Body
	}
	t.UpdatedAtMs = atMs
	return t
}

// cloneTemplate deep-copies the body slices (the memory store must not alias
// a caller's slices).
func cloneTemplate(t wire.RoomTemplate) wire.RoomTemplate {
	b, _ := json.Marshal(t.Body)
	var body wire.RoomTemplateBody
	_ = json.Unmarshal(b, &body)
	t.Body = normalizeTemplateBody(body)
	return t
}

// normalizeTemplateBody turns nil lists into empty ones so the wire shape is
// stable ([] rather than null).
func normalizeTemplateBody(b wire.RoomTemplateBody) wire.RoomTemplateBody {
	if b.Channels == nil {
		b.Channels = []string{}
	}
	if b.PromptPacks == nil {
		b.PromptPacks = []wire.TemplateContentUse{}
	}
	if b.SuggestedAgents == nil {
		b.SuggestedAgents = []wire.TemplateAgent{}
	}
	if b.DefaultMembers == nil {
		b.DefaultMembers = []string{}
	}
	if b.Schedules == nil {
		b.Schedules = []wire.TemplateSchedule{}
	}
	return b
}

// --- memory ---

// MemoryRoomTemplates is the in-memory RoomTemplateStore (dev / tests).
type MemoryRoomTemplates struct {
	mu   sync.Mutex
	rows map[string]wire.RoomTemplate // tenant\x00templateID -> template
}

// NewMemoryRoomTemplates constructs an empty in-memory template store.
func NewMemoryRoomTemplates() *MemoryRoomTemplates {
	return &MemoryRoomTemplates{rows: map[string]wire.RoomTemplate{}}
}

var _ RoomTemplateStore = (*MemoryRoomTemplates)(nil)

func (m *MemoryRoomTemplates) CreateTemplate(ctx context.Context, t wire.RoomTemplate) (wire.RoomTemplate, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	t.TenantID = tid
	t = cloneTemplate(t)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, cur := range m.rows {
		if cur.TemplateID == t.TemplateID {
			return wire.RoomTemplate{}, ErrTemplateExists
		}
	}
	m.rows[tid+"\x00"+t.TemplateID] = t
	return cloneTemplate(t), nil
}

func (m *MemoryRoomTemplates) ListTemplates(ctx context.Context, viewer string, mineOnly bool) ([]wire.RoomTemplate, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []wire.RoomTemplate{}
	for _, t := range m.rows {
		if t.TenantID != tid || !templateVisible(t, viewer) || (mineOnly && t.CreatedBy != viewer) {
			continue
		}
		out = append(out, cloneTemplate(t))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAtMs != out[j].UpdatedAtMs {
			return out[i].UpdatedAtMs > out[j].UpdatedAtMs
		}
		return out[i].TemplateID < out[j].TemplateID
	})
	if len(out) > MaxTemplateList {
		out = out[:MaxTemplateList]
	}
	return out, nil
}

func (m *MemoryRoomTemplates) visibleLocked(tid, templateID, viewer string) (wire.RoomTemplate, error) {
	t, ok := m.rows[tid+"\x00"+templateID]
	if !ok || !templateVisible(t, viewer) {
		return wire.RoomTemplate{}, ErrTemplateNotFound
	}
	return t, nil
}

func (m *MemoryRoomTemplates) GetTemplate(ctx context.Context, templateID, viewer string) (wire.RoomTemplate, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.visibleLocked(tid, templateID, viewer)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	return cloneTemplate(t), nil
}

func (m *MemoryRoomTemplates) UpdateTemplate(ctx context.Context, templateID, requester string, patch TemplatePatch, atMs int64) (wire.RoomTemplate, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.visibleLocked(tid, templateID, requester)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	if t.CreatedBy != requester {
		return wire.RoomTemplate{}, ErrTemplateForbidden
	}
	t = cloneTemplate(applyTemplatePatch(t, patch, atMs))
	m.rows[tid+"\x00"+templateID] = t
	return cloneTemplate(t), nil
}

func (m *MemoryRoomTemplates) DeleteTemplate(ctx context.Context, templateID, requester string, moderator bool) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.visibleLocked(tid, templateID, requester)
	if err != nil {
		return err
	}
	if t.CreatedBy != requester && !(moderator && t.Visibility == wire.TemplateTenant) {
		return ErrTemplateForbidden
	}
	delete(m.rows, tid+"\x00"+templateID)
	return nil
}

func (m *MemoryRoomTemplates) UseTemplate(ctx context.Context, templateID, viewer string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.visibleLocked(tid, templateID, viewer)
	if err != nil {
		return err
	}
	t.UseCount++
	m.rows[tid+"\x00"+templateID] = t
	return nil
}

// --- MySQL ---

// MySQLRoomTemplates is the MySQL-backed RoomTemplateStore.
type MySQLRoomTemplates struct{ DB *sql.DB }

// NewMySQLRoomTemplates constructs a MySQL template store.
func NewMySQLRoomTemplates(db *sql.DB) *MySQLRoomTemplates { return &MySQLRoomTemplates{DB: db} }

var _ RoomTemplateStore = (*MySQLRoomTemplates)(nil)

const templateCols = `template_id, tenant_id, name, description, visibility, created_by, body_json, use_count, created_at_ms, updated_at_ms`

func scanTemplate(row rowScanner) (wire.RoomTemplate, error) {
	var t wire.RoomTemplate
	var body []byte
	if err := row.Scan(&t.TemplateID, &t.TenantID, &t.Name, &t.Description, &t.Visibility, &t.CreatedBy,
		&body, &t.UseCount, &t.CreatedAtMs, &t.UpdatedAtMs); err != nil {
		return wire.RoomTemplate{}, err
	}
	if err := json.Unmarshal(body, &t.Body); err != nil {
		return wire.RoomTemplate{}, err
	}
	t.Body = normalizeTemplateBody(t.Body)
	return t, nil
}

func (m *MySQLRoomTemplates) CreateTemplate(ctx context.Context, t wire.RoomTemplate) (wire.RoomTemplate, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	t.TenantID = tid
	t.Body = normalizeTemplateBody(t.Body)
	body, err := jsonArg(t.Body)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO room_templates (`+templateCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		t.TemplateID, tid, t.Name, t.Description, t.Visibility, t.CreatedBy, body, t.UseCount, t.CreatedAtMs, t.UpdatedAtMs)
	if err != nil {
		return wire.RoomTemplate{}, dupAs(err, ErrTemplateExists)
	}
	return t, nil
}

func (m *MySQLRoomTemplates) ListTemplates(ctx context.Context, viewer string, mineOnly bool) ([]wire.RoomTemplate, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + templateCols + ` FROM room_templates WHERE tenant_id = ? AND (created_by = ? OR visibility = ?)`
	args := []any{tid, viewer, wire.TemplateTenant}
	if mineOnly {
		q += ` AND created_by = ?`
		args = append(args, viewer)
	}
	q += ` ORDER BY updated_at_ms DESC, template_id LIMIT ?`
	return queryAll(ctx, m.DB, q, scanTemplate, append(args, MaxTemplateList)...)
}

func (m *MySQLRoomTemplates) visible(ctx context.Context, tid, templateID, viewer string) (wire.RoomTemplate, error) {
	t, err := scanTemplate(m.DB.QueryRowContext(ctx, `SELECT `+templateCols+` FROM room_templates
		WHERE tenant_id = ? AND template_id = ?`, tid, templateID))
	if errors.Is(err, sql.ErrNoRows) {
		return wire.RoomTemplate{}, ErrTemplateNotFound
	}
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	if !templateVisible(t, viewer) {
		return wire.RoomTemplate{}, ErrTemplateNotFound
	}
	return t, nil
}

func (m *MySQLRoomTemplates) GetTemplate(ctx context.Context, templateID, viewer string) (wire.RoomTemplate, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	return m.visible(ctx, tid, templateID, viewer)
}

func (m *MySQLRoomTemplates) UpdateTemplate(ctx context.Context, templateID, requester string, patch TemplatePatch, atMs int64) (wire.RoomTemplate, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	t, err := m.visible(ctx, tid, templateID, requester)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	if t.CreatedBy != requester {
		return wire.RoomTemplate{}, ErrTemplateForbidden
	}
	t = applyTemplatePatch(t, patch, atMs)
	t.Body = normalizeTemplateBody(t.Body)
	body, err := jsonArg(t.Body)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	// The author guard is repeated in the WHERE so the write itself can never
	// touch another member's row.
	res, err := m.DB.ExecContext(ctx, `UPDATE room_templates SET name = ?, description = ?, visibility = ?, body_json = ?, updated_at_ms = ?
		WHERE tenant_id = ? AND template_id = ? AND created_by = ?`,
		t.Name, t.Description, t.Visibility, body, t.UpdatedAtMs, tid, templateID, requester)
	if err != nil {
		return wire.RoomTemplate{}, err
	}
	if err := requireMatched(res, ErrTemplateNotFound); err != nil {
		return wire.RoomTemplate{}, err
	}
	return t, nil
}

func (m *MySQLRoomTemplates) DeleteTemplate(ctx context.Context, templateID, requester string, moderator bool) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	t, err := m.visible(ctx, tid, templateID, requester)
	if err != nil {
		return err
	}
	if t.CreatedBy != requester && !(moderator && t.Visibility == wire.TemplateTenant) {
		return ErrTemplateForbidden
	}
	_, err = m.DB.ExecContext(ctx, `DELETE FROM room_templates WHERE tenant_id = ? AND template_id = ?`, tid, templateID)
	return err
}

func (m *MySQLRoomTemplates) UseTemplate(ctx context.Context, templateID, viewer string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(ctx, `UPDATE room_templates SET use_count = use_count + 1
		WHERE tenant_id = ? AND template_id = ? AND (created_by = ? OR visibility = ?)`, tid, templateID, viewer, wire.TemplateTenant)
	if err != nil {
		return err
	}
	return requireMatched(res, ErrTemplateNotFound)
}
