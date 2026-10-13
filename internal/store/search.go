package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Global search (F7) and single-message lookup over the persisted room feed
// and room artifacts.
//
// MySQL indexes message text in room_messages.search_text (written by
// AppendRoomMessage / EditRoomMessage from the payload) with a FULLTEXT index
// using the built-in ngram parser, so CJK text (no word delimiters) is
// searchable. Queries run as a quoted BOOLEAN MODE phrase: with the ngram
// parser a phrase is the consecutive n-gram sequence, i.e. substring match for
// terms of at least ngram_token_size (default 2) characters — the API rejects
// shorter queries (SearchMinRunes). The index is created with stopwords
// disabled (innodb_ft_enable_stopword=OFF): the ngram parser drops every token
// that CONTAINS a stopword, and the default English list holds "a" and "i",
// which would make most English bigrams unsearchable. The in-memory backend
// matches case-insensitive substrings, the same observable behaviour.

// SearchMinRunes is the shortest query searched: the ngram parser's default
// token size. Shorter queries cannot match the n-gram index.
const SearchMinRunes = 2

// MessageSearch scopes one message search: rooms the caller may read (the
// caller resolves membership; the store never widens it), the query text, an
// optional created_at_ms cursor (only older hits), and a page size
// (PageSize).
type MessageSearch struct {
	RoomIDs  []string
	Text     string
	BeforeMs int64
	Limit    int
}

// ChatIndex is the read side of the room feed beyond history paging: one
// message by sequence and full-text search across rooms. Deleted messages are
// never returned.
type ChatIndex interface {
	// GetRoomMessage returns one live (not soft-deleted) message by its room
	// sequence; found=false when missing or deleted.
	GetRoomMessage(ctx context.Context, roomID string, seq int64) (m RoomMessage, found bool, err error)
	// SearchRoomMessages returns live messages in s.RoomIDs whose text
	// contains s.Text, newest first (created_at, then seq, then room id,
	// descending); an unsearchable query (Searchable) answers [].
	SearchRoomMessages(ctx context.Context, s MessageSearch) ([]RoomMessage, error)
	// LoadSearchSources and ScanSearchSources read rows for an external
	// search index, by key and as a reindex walk; cross-tenant
	// (search_source.go).
	LoadSearchSources(ctx context.Context, keys []MessageKey) ([]SearchSource, error)
	ScanSearchSources(ctx context.Context, cursor string, batch int) ([]SearchSource, SearchReindexBatch, error)
}

// ArtifactIndex searches room artifacts by title and file name.
type ArtifactIndex interface {
	// SearchArtifacts returns artifacts in roomIDs whose title or file name
	// contains text (taken literally), most recently updated first (then
	// artifact id, descending), at most clampLimit(limit, DefaultSearchPage,
	// MaxSearchPage); an empty text or room list answers [].
	SearchArtifacts(ctx context.Context, roomIDs []string, text string, limit int) ([]Artifact, error)
}

// MessageText extracts the searchable/displayable text of a persisted feed
// payload: a chat message's text, or the newline-joined texts of an
// agent_messages batch.
func MessageText(kind string, payload json.RawMessage) string {
	switch kind {
	case wire.KindChatMessage:
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(payload, &p)
		return p.Text
	case wire.KindAgentMessages:
		var p struct {
			Messages []struct {
				Payload struct {
					Text string `json:"text"`
				} `json:"payload"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(payload, &p)
		parts := make([]string, 0, len(p.Messages))
		for _, m := range p.Messages {
			if t := strings.TrimSpace(m.Payload.Text); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// MessageAuthor labels who wrote a persisted feed message: the human author of
// a chat message, or "owner/provider" for an agent reply (whose row carries no
// author column).
func MessageAuthor(m RoomMessage) string {
	if m.Author != "" || m.Kind != wire.KindAgentMessages {
		return m.Author
	}
	var p struct {
		Messages []struct {
			Payload struct {
				OwnerUserID string `json:"ownerUserId"`
				Provider    string `json:"provider"`
			} `json:"payload"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(m.Payload, &p)
	for _, msg := range p.Messages {
		owner, provider := msg.Payload.OwnerUserID, msg.Payload.Provider
		switch {
		case owner != "" && provider != "":
			return owner + "/" + provider
		case provider != "":
			return provider
		}
	}
	return "agent"
}

// containsFold reports a case-insensitive substring match.
func containsFold(hay, needle string) bool {
	return strings.Contains(strings.ToLower(hay), strings.ToLower(needle))
}

// likePattern builds a LIKE '%text%' pattern with the LIKE metacharacters
// escaped (backslash is MySQL's default LIKE escape).
func likePattern(text string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(text) + "%"
}

// fulltextPhrase quotes text as one BOOLEAN MODE phrase. Inside quotes the
// boolean operators are literal; the only character that must not appear is
// the quote itself.
func fulltextPhrase(text string) string {
	return `"` + strings.ReplaceAll(text, `"`, " ") + `"`
}

// Search page sizes (messages and artifacts).
const (
	DefaultSearchPage = 20
	MaxSearchPage     = 100
)

func searchLimit(n int) int { return clampLimit(n, DefaultSearchPage, MaxSearchPage) }

// inPlaceholders returns "?,?,…" for n arguments.
func inPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Searchable reports whether q can match anything: at least one room and a
// query of at least SearchMinRunes. Every backend answers an unsearchable
// query with no results.
func (q MessageSearch) Searchable() bool {
	return len(q.RoomIDs) > 0 && utf8.RuneCountInString(strings.TrimSpace(q.Text)) >= SearchMinRunes
}

// PageSize is clampLimit(q.Limit, DefaultSearchPage, MaxSearchPage).
func (q MessageSearch) PageSize() int { return searchLimit(q.Limit) }

// Admits reports whether a stored row passes the scope SearchRoomMessages
// applies in SQL — the caller's tenant, one of q.RoomIDs, not soft-deleted,
// older than the BeforeMs cursor, carrying search text — leaving only the
// text match to the index. An external index (internal/search) re-checks
// every candidate it returns with it, so its results can never be wider
// than the MySQL query's. Keep it in step with SearchRoomMessages.
func (q MessageSearch) Admits(tenantID string, src SearchSource) bool {
	if src.TenantID != tenantID || src.Deleted || src.Text() == "" {
		return false
	}
	if q.BeforeMs > 0 && src.CreatedAtMs >= q.BeforeMs {
		return false
	}
	for _, id := range q.RoomIDs {
		if id == src.RoomID {
			return true
		}
	}
	return false
}

// --- MemoryChat ---

var _ ChatIndex = (*MemoryChat)(nil)

// deletedKey is the s.deleted key of message seq of the room stored under rk.
func deletedKey(rk string, seq int64) string { return rk + "\x00" + strconv.FormatInt(seq, 10) }

// isDeleted is the memory side of sqlNotDeleted.
func (s *MemoryChat) isDeleted(rk string, seq int64) bool { return s.deleted[deletedKey(rk, seq)] }

// GetRoomMessage returns one live message by sequence.
func (s *MemoryChat) GetRoomMessage(ctx context.Context, roomID string, seq int64) (RoomMessage, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomMessage{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tid + "\x00" + roomID
	for _, m := range s.msgs[rk] {
		if m.Seq == seq && !s.isDeleted(rk, seq) {
			return m, true, nil
		}
	}
	return RoomMessage{}, false, nil
}

// SearchRoomMessages substring-matches message text across the given rooms.
func (s *MemoryChat) SearchRoomMessages(ctx context.Context, q MessageSearch) ([]RoomMessage, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if !q.Searchable() {
		return []RoomMessage{}, nil
	}
	text := strings.TrimSpace(q.Text)
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []RoomMessage{}
	for _, roomID := range q.RoomIDs {
		rk := tid + "\x00" + roomID
		for _, m := range s.msgs[rk] {
			if s.isDeleted(rk, m.Seq) || (q.BeforeMs > 0 && m.CreatedAtMs >= q.BeforeMs) {
				continue
			}
			if containsFold(MessageText(m.Kind, m.Payload), text) {
				out = append(out, m)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs > out[j].CreatedAtMs
		}
		if out[i].Seq != out[j].Seq {
			return out[i].Seq > out[j].Seq
		}
		return out[i].RoomID > out[j].RoomID
	})
	if limit := q.PageSize(); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- MySQLChat ---

var _ ChatIndex = (*MySQLChat)(nil)

const roomMessageCols = `room_id, COALESCE(channel_id, ''), seq, kind, COALESCE(author,''), payload_json, created_at_ms, edited_at_ms`

func scanRoomMessage(row rowScanner) (RoomMessage, error) {
	var m RoomMessage
	var payload []byte
	var editedAt sql.NullInt64
	if err := row.Scan(&m.RoomID, &m.ChannelID, &m.Seq, &m.Kind, &m.Author, &payload, &m.CreatedAtMs, &editedAt); err != nil {
		return RoomMessage{}, err
	}
	m.Payload = json.RawMessage(payload)
	if editedAt.Valid {
		v := editedAt.Int64
		m.EditedAtMs = &v
	}
	return m, nil
}

// GetRoomMessage returns one live message by sequence.
func (s *MySQLChat) GetRoomMessage(ctx context.Context, roomID string, seq int64) (RoomMessage, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomMessage{}, false, err
	}
	m, err := scanRoomMessage(s.DB.QueryRowContext(ctx,
		`SELECT `+roomMessageCols+` FROM room_messages
		 WHERE tenant_id = ? AND room_id = ? AND seq = ? AND `+sqlNotDeleted, tid, roomID, seq))
	if err == sql.ErrNoRows {
		return RoomMessage{}, false, nil
	}
	if err != nil {
		return RoomMessage{}, false, err
	}
	return m, true, nil
}

// SearchRoomMessages runs a FULLTEXT (ngram) phrase match over search_text in
// the given rooms, newest first. MySQL evaluates the MATCH over the whole
// table before the tenant and room filters, so its cost grows with the
// deployment; docs/production-deployment.md says when OpenSearch is required.
func (s *MySQLChat) SearchRoomMessages(ctx context.Context, q MessageSearch) ([]RoomMessage, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if !q.Searchable() {
		return []RoomMessage{}, nil
	}
	args := []any{tid}
	for _, id := range q.RoomIDs {
		args = append(args, id)
	}
	query := `SELECT ` + roomMessageCols + ` FROM room_messages
	 WHERE tenant_id = ? AND room_id IN (` + inPlaceholders(len(q.RoomIDs)) + `) AND ` + sqlNotDeleted + `
	   AND MATCH(search_text) AGAINST (? IN BOOLEAN MODE)`
	args = append(args, fulltextPhrase(strings.TrimSpace(q.Text)))
	if q.BeforeMs > 0 {
		query += ` AND created_at_ms < ?`
		args = append(args, q.BeforeMs)
	}
	query += ` ORDER BY created_at_ms DESC, seq DESC, room_id DESC LIMIT ?`
	return queryAll(ctx, s.DB, query, scanRoomMessage, append(args, q.PageSize())...)
}

// --- artifacts ---

var _ ArtifactIndex = (*MemoryArtifacts)(nil)
var _ ArtifactIndex = (*MySQLArtifacts)(nil)

// SearchArtifacts substring-matches artifact titles and file names in the
// given rooms.
func (s *MemoryArtifacts) SearchArtifacts(ctx context.Context, roomIDs []string, text string, limit int) ([]Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if len(roomIDs) == 0 || text == "" {
		return []Artifact{}, nil
	}
	in := make(map[string]bool, len(roomIDs))
	for _, id := range roomIDs {
		in[id] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Artifact{}
	for _, a := range s.artifacts {
		if a.TenantID == tid && in[a.RoomID] && (containsFold(a.Title, text) || containsFold(a.FileName, text)) {
			out = append(out, s.withAppFactsLocked(a))
		}
	}
	sortArtifactsNewestFirst(out)
	if limit = searchLimit(limit); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SearchArtifacts LIKE-matches artifact titles and file names in the given
// rooms. Both are short and the set is bounded by the caller's rooms, so no
// text index is needed.
func (m *MySQLArtifacts) SearchArtifacts(ctx context.Context, roomIDs []string, text string, limit int) ([]Artifact, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	text = strings.TrimSpace(text)
	if len(roomIDs) == 0 || text == "" {
		return []Artifact{}, nil
	}
	args := []any{tid}
	for _, id := range roomIDs {
		args = append(args, id)
	}
	args = append(args, likePattern(text), likePattern(text), searchLimit(limit))
	return queryAll(ctx, m.DB, `SELECT `+artifactCols+` FROM artifacts a
	 WHERE a.tenant_id = ? AND a.room_id IN (`+inPlaceholders(len(roomIDs))+`) AND (a.title LIKE ? OR a.file_name LIKE ?)
	 ORDER BY a.updated_at_ms DESC, a.artifact_id DESC LIMIT ?`, scanArtifact, args...)
}
