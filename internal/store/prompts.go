package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Prompt library (F2): tenant-scoped prompt entries saved from room messages.
// A prompt is visible to the viewer when the viewer authored it or its
// visibility is tenant. Text search is a case-insensitive substring match on
// title and body (LIKE in MySQL): a tenant's library is small and bounded,
// unlike the room feed, so it needs no text index.

// ErrPromptNotFound reports a prompt that does not exist or that the viewer
// cannot see (another member's private prompt reads as missing).
var ErrPromptNotFound = errors.New("store: prompt not found")

// ErrPromptForbidden reports a visible prompt the requester may not change.
var ErrPromptForbidden = errors.New("store: prompt not owned by requester")

// PromptQuery filters a library listing for one viewer.
type PromptQuery struct {
	Viewer   string
	Text     string
	Tag      string
	MineOnly bool
	Limit    int
}

// PromptPatch is a partial prompt update; nil fields are unchanged.
type PromptPatch struct {
	Title      *string
	Body       *string
	Tags       *[]string
	Visibility *string
}

// PromptStore persists the prompt library. Every method is tenant-scoped by
// the context's tenant.
type PromptStore interface {
	CreatePrompt(ctx context.Context, p wire.Prompt) (wire.Prompt, error)
	// ListPrompts returns prompts visible to q.Viewer, most recently updated
	// first.
	ListPrompts(ctx context.Context, q PromptQuery) ([]wire.Prompt, error)
	// UpdatePrompt applies patch when requester authored the prompt.
	UpdatePrompt(ctx context.Context, promptID, requester string, patch PromptPatch, atMs int64) (wire.Prompt, error)
	// DeletePrompt removes a prompt requester authored, or — when moderator —
	// any tenant-visible prompt.
	DeletePrompt(ctx context.Context, promptID, requester string, moderator bool) error
	// UsePrompt increments the use counter of a prompt visible to viewer.
	UsePrompt(ctx context.Context, promptID, viewer string) (wire.Prompt, error)
}

func promptVisible(p wire.Prompt, viewer string) bool {
	return p.Author == viewer || p.Visibility == wire.PromptTenant
}

func promptMatches(p wire.Prompt, q PromptQuery) bool {
	if !promptVisible(p, q.Viewer) || (q.MineOnly && p.Author != q.Viewer) {
		return false
	}
	if q.Tag != "" && !containsString(p.Tags, q.Tag) {
		return false
	}
	if t := strings.TrimSpace(q.Text); t != "" && !containsFold(p.Title, t) && !containsFold(p.Body, t) {
		return false
	}
	return true
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// applyPromptPatch returns p with the patch's set fields applied.
func applyPromptPatch(p wire.Prompt, patch PromptPatch, atMs int64) wire.Prompt {
	if patch.Title != nil {
		p.Title = *patch.Title
	}
	if patch.Body != nil {
		p.Body = *patch.Body
	}
	if patch.Tags != nil {
		p.Tags = append([]string{}, (*patch.Tags)...)
	}
	if patch.Visibility != nil {
		p.Visibility = *patch.Visibility
	}
	p.UpdatedAtMs = atMs
	return p
}

// --- memory ---

// MemoryPrompts is the in-memory PromptStore (dev / tests).
type MemoryPrompts struct {
	mu   sync.Mutex
	rows map[string]wire.Prompt // tenant\x00promptID -> prompt
}

// NewMemoryPrompts constructs an empty in-memory prompt store.
func NewMemoryPrompts() *MemoryPrompts { return &MemoryPrompts{rows: map[string]wire.Prompt{}} }

var _ PromptStore = (*MemoryPrompts)(nil)

func (s *MemoryPrompts) CreatePrompt(ctx context.Context, p wire.Prompt) (wire.Prompt, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.Prompt{}, err
	}
	p.TenantID = tid
	p.Tags = append([]string{}, p.Tags...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[tid+"\x00"+p.PromptID] = p
	return p, nil
}

func (s *MemoryPrompts) ListPrompts(ctx context.Context, q PromptQuery) ([]wire.Prompt, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []wire.Prompt{}
	for _, p := range s.rows {
		if p.TenantID == tid && promptMatches(p, q) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAtMs != out[j].UpdatedAtMs {
			return out[i].UpdatedAtMs > out[j].UpdatedAtMs
		}
		return out[i].PromptID < out[j].PromptID
	})
	if limit := searchLimit(q.Limit); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// visibleLocked returns the prompt when viewer may see it. Caller holds mu.
func (s *MemoryPrompts) visibleLocked(tid, promptID, viewer string) (wire.Prompt, error) {
	p, ok := s.rows[tid+"\x00"+promptID]
	if !ok || !promptVisible(p, viewer) {
		return wire.Prompt{}, ErrPromptNotFound
	}
	return p, nil
}

func (s *MemoryPrompts) UpdatePrompt(ctx context.Context, promptID, requester string, patch PromptPatch, atMs int64) (wire.Prompt, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.Prompt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.visibleLocked(tid, promptID, requester)
	if err != nil {
		return wire.Prompt{}, err
	}
	if p.Author != requester {
		return wire.Prompt{}, ErrPromptForbidden
	}
	p = applyPromptPatch(p, patch, atMs)
	s.rows[tid+"\x00"+promptID] = p
	return p, nil
}

func (s *MemoryPrompts) DeletePrompt(ctx context.Context, promptID, requester string, moderator bool) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.visibleLocked(tid, promptID, requester)
	if err != nil {
		return err
	}
	if p.Author != requester && !(moderator && p.Visibility == wire.PromptTenant) {
		return ErrPromptForbidden
	}
	delete(s.rows, tid+"\x00"+promptID)
	return nil
}

func (s *MemoryPrompts) UsePrompt(ctx context.Context, promptID, viewer string) (wire.Prompt, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.Prompt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.visibleLocked(tid, promptID, viewer)
	if err != nil {
		return wire.Prompt{}, err
	}
	p.UseCount++
	s.rows[tid+"\x00"+promptID] = p
	return p, nil
}

// --- MySQL ---

// MySQLPrompts is the MySQL-backed PromptStore.
type MySQLPrompts struct{ DB *sql.DB }

// NewMySQLPrompts constructs a MySQL prompt store.
func NewMySQLPrompts(db *sql.DB) *MySQLPrompts { return &MySQLPrompts{DB: db} }

var _ PromptStore = (*MySQLPrompts)(nil)

const promptCols = `prompt_id, tenant_id, author, title, body, tags_json, visibility, source_room_id, source_seq, use_count, created_at_ms, updated_at_ms`

func scanPrompt(row interface{ Scan(...any) error }) (wire.Prompt, error) {
	var p wire.Prompt
	var tags []byte
	if err := row.Scan(&p.PromptID, &p.TenantID, &p.Author, &p.Title, &p.Body, &tags, &p.Visibility,
		&p.SourceRoomID, &p.SourceSeq, &p.UseCount, &p.CreatedAtMs, &p.UpdatedAtMs); err != nil {
		return wire.Prompt{}, err
	}
	p.Tags = []string{}
	if len(tags) > 0 {
		if err := json.Unmarshal(tags, &p.Tags); err != nil {
			return wire.Prompt{}, err
		}
	}
	return p, nil
}

func marshalTags(tags []string) ([]byte, error) {
	if tags == nil {
		tags = []string{}
	}
	return json.Marshal(tags)
}

func (s *MySQLPrompts) CreatePrompt(ctx context.Context, p wire.Prompt) (wire.Prompt, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.Prompt{}, err
	}
	p.TenantID = tid
	tags, err := marshalTags(p.Tags)
	if err != nil {
		return wire.Prompt{}, err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO prompts (`+promptCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.PromptID, tid, p.Author, p.Title, p.Body, tags, p.Visibility, p.SourceRoomID, p.SourceSeq, p.UseCount, p.CreatedAtMs, p.UpdatedAtMs)
	if err != nil {
		return wire.Prompt{}, err
	}
	return p, nil
}

func (s *MySQLPrompts) ListPrompts(ctx context.Context, q PromptQuery) ([]wire.Prompt, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + promptCols + ` FROM prompts WHERE tenant_id = ? AND (author = ? OR visibility = ?)`
	args := []any{tid, q.Viewer, wire.PromptTenant}
	if q.MineOnly {
		query += ` AND author = ?`
		args = append(args, q.Viewer)
	}
	if q.Tag != "" {
		query += ` AND JSON_CONTAINS(tags_json, JSON_QUOTE(?))`
		args = append(args, q.Tag)
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		query += ` AND (title LIKE ? OR body LIKE ?)`
		args = append(args, likePattern(t), likePattern(t))
	}
	query += ` ORDER BY updated_at_ms DESC, prompt_id LIMIT ?`
	args = append(args, searchLimit(q.Limit))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []wire.Prompt{}
	for rows.Next() {
		p, err := scanPrompt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// visible loads a prompt the viewer may see.
func (s *MySQLPrompts) visible(ctx context.Context, tid, promptID, viewer string) (wire.Prompt, error) {
	p, err := scanPrompt(s.DB.QueryRowContext(ctx, `SELECT `+promptCols+` FROM prompts WHERE tenant_id = ? AND prompt_id = ?`, tid, promptID))
	if errors.Is(err, sql.ErrNoRows) {
		return wire.Prompt{}, ErrPromptNotFound
	}
	if err != nil {
		return wire.Prompt{}, err
	}
	if !promptVisible(p, viewer) {
		return wire.Prompt{}, ErrPromptNotFound
	}
	return p, nil
}

func (s *MySQLPrompts) UpdatePrompt(ctx context.Context, promptID, requester string, patch PromptPatch, atMs int64) (wire.Prompt, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.Prompt{}, err
	}
	p, err := s.visible(ctx, tid, promptID, requester)
	if err != nil {
		return wire.Prompt{}, err
	}
	if p.Author != requester {
		return wire.Prompt{}, ErrPromptForbidden
	}
	p = applyPromptPatch(p, patch, atMs)
	tags, err := marshalTags(p.Tags)
	if err != nil {
		return wire.Prompt{}, err
	}
	// The author guard is repeated in the WHERE so the write itself can never
	// touch another member's row.
	res, err := s.DB.ExecContext(ctx, `UPDATE prompts SET title = ?, body = ?, tags_json = ?, visibility = ?, updated_at_ms = ?
		WHERE tenant_id = ? AND prompt_id = ? AND author = ?`,
		p.Title, p.Body, tags, p.Visibility, p.UpdatedAtMs, tid, promptID, requester)
	if err != nil {
		return wire.Prompt{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return wire.Prompt{}, ErrPromptNotFound
	}
	return p, nil
}

func (s *MySQLPrompts) DeletePrompt(ctx context.Context, promptID, requester string, moderator bool) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	p, err := s.visible(ctx, tid, promptID, requester)
	if err != nil {
		return err
	}
	if p.Author != requester && !(moderator && p.Visibility == wire.PromptTenant) {
		return ErrPromptForbidden
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM prompts WHERE tenant_id = ? AND prompt_id = ?`, tid, promptID)
	return err
}

func (s *MySQLPrompts) UsePrompt(ctx context.Context, promptID, viewer string) (wire.Prompt, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.Prompt{}, err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE prompts SET use_count = use_count + 1
		WHERE tenant_id = ? AND prompt_id = ? AND (author = ? OR visibility = ?)`, tid, promptID, viewer, wire.PromptTenant)
	if err != nil {
		return wire.Prompt{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return wire.Prompt{}, ErrPromptNotFound
	}
	return s.visible(ctx, tid, promptID, viewer)
}
