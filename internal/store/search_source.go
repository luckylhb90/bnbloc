package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Source rows for an external search index (internal/search). The store is
// the authority over room messages; an external index only holds a copy it
// rebuilds from these reads: by key after a write (publish, edit, delete),
// and as a primary-key walk for a reindex. Both are cross-tenant (index
// maintenance is a platform operation); every row carries its tenant, and
// query-time reads re-check tenant, room and visibility on the result.

// MessageKey names one room message across tenants.
type MessageKey struct {
	TenantID string
	RoomID   string
	Seq      int64
}

// SearchSource is one stored room message as an external index copies it:
// the row with its TenantID set, the kind of its channel ("people" for the
// people chat, "" for an ordinary channel or the legacy empty channel), and
// whether it is soft-deleted.
type SearchSource struct {
	RoomMessage
	ChannelKind string
	Deleted     bool
}

// Text is the searchable text of the row (MessageText), "" for a deleted
// row, a retention fence or a kind that carries no text.
func (s SearchSource) Text() string {
	if s.Deleted {
		return ""
	}
	return MessageText(s.Kind, s.Payload)
}

// searchSourceLoadChunk bounds the keys of one LoadSearchSources statement.
const searchSourceLoadChunk = 500

// --- MemoryChat ---

// LoadSearchSources returns the stored rows of keys (soft-deleted included,
// missing keys omitted). The memory store has no channel registry, so
// ChannelKind is always "".
func (s *MemoryChat) LoadSearchSources(_ context.Context, keys []MessageKey) ([]SearchSource, error) {
	want := map[string]map[int64]bool{}
	for _, k := range keys {
		rk := k.TenantID + "\x00" + k.RoomID
		if want[rk] == nil {
			want[rk] = map[int64]bool{}
		}
		want[rk][k.Seq] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SearchSource
	for rk, seqs := range want {
		for _, m := range s.msgs[rk] {
			if seqs[m.Seq] {
				out = append(out, s.sourceLocked(rk, m))
			}
		}
	}
	return out, nil
}

// ScanSearchSources reads one batch of rows after cursor in the reindex
// order ((room, seq), the same cursor contract as ReindexSearchText).
func (s *MemoryChat) ScanSearchSources(_ context.Context, cursor string, batch int) ([]SearchSource, SearchReindexBatch, error) {
	from, err := parseSearchCursor(cursor)
	if err != nil {
		return nil, SearchReindexBatch{}, err
	}
	batch = reindexBatchSize(batch)
	s.mu.Lock()
	var rows []SearchSource
	for rk, msgs := range s.msgs {
		roomID := rk[strings.LastIndex(rk, "\x00")+1:]
		for _, m := range msgs {
			if from.less(roomID, m.Seq) {
				rows = append(rows, s.sourceLocked(rk, m))
			}
		}
	}
	s.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool {
		return searchCursor{rows[i].RoomID, rows[i].Seq}.less(rows[j].RoomID, rows[j].Seq)
	})
	out := SearchReindexBatch{Cursor: cursor, Done: len(rows) < batch}
	if len(rows) > batch {
		rows = rows[:batch]
	}
	out.Scanned = len(rows)
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		out.Cursor = searchCursor{last.RoomID, last.Seq}.String()
	}
	return rows, out, nil
}

// sourceLocked builds the source row of m stored under rk; the caller holds
// mu.
func (s *MemoryChat) sourceLocked(rk string, m RoomMessage) SearchSource {
	i := strings.LastIndex(rk, "\x00")
	m.TenantID = rk[:i]
	return SearchSource{RoomMessage: m, Deleted: s.deleted[rk+"\x00"+strconv.FormatInt(m.Seq, 10)]}
}

// --- MySQLChat ---

// searchSourceSelect reads a room message with its channel's kind. The
// channel row is a primary-key lookup; the legacy empty channel has none.
const searchSourceSelect = `SELECT m.tenant_id, m.room_id, m.channel_id, m.seq, m.kind, COALESCE(m.author, ''),
       m.payload_json, m.created_at_ms, m.edited_at_ms, m.deleted_at_ms IS NOT NULL, COALESCE(c.kind, '')
  FROM room_messages m
  LEFT JOIN room_channels c ON c.channel_id = m.channel_id AND c.room_id = m.room_id AND m.channel_id <> ''`

func scanSearchSource(row interface{ Scan(...any) error }) (SearchSource, error) {
	var s SearchSource
	var payload []byte
	var editedAt sql.NullInt64
	if err := row.Scan(&s.TenantID, &s.RoomID, &s.ChannelID, &s.Seq, &s.Kind, &s.Author,
		&payload, &s.CreatedAtMs, &editedAt, &s.Deleted, &s.ChannelKind); err != nil {
		return SearchSource{}, err
	}
	s.Payload = json.RawMessage(payload)
	if editedAt.Valid {
		v := editedAt.Int64
		s.EditedAtMs = &v
	}
	return s, nil
}

// LoadSearchSources returns the stored rows of keys (soft-deleted included,
// missing keys omitted), grouped per room so each room's sequences are one
// primary-key range lookup. A row stored under another tenant than its key
// names is omitted.
func (s *MySQLChat) LoadSearchSources(ctx context.Context, keys []MessageKey) ([]SearchSource, error) {
	var out []SearchSource
	for len(keys) > 0 {
		n := min(len(keys), searchSourceLoadChunk)
		rows, err := s.loadSearchSources(ctx, keys[:n])
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		keys = keys[n:]
	}
	return out, nil
}

func (s *MySQLChat) loadSearchSources(ctx context.Context, keys []MessageKey) ([]SearchSource, error) {
	want := make(map[MessageKey]bool, len(keys))
	seqs := map[string][]any{}
	for _, k := range keys {
		if !want[k] {
			want[k] = true
			seqs[k.RoomID] = append(seqs[k.RoomID], k.Seq)
		}
	}
	rooms := sortedRoomKeys(seqs)
	var where []string
	var args []any
	for _, room := range rooms {
		where = append(where, `(m.room_id = ? AND m.seq IN (`+inPlaceholders(len(seqs[room]))+`))`)
		args = append(append(args, room), seqs[room]...)
	}
	rows, err := s.DB.QueryContext(ctx, searchSourceSelect+` WHERE `+strings.Join(where, " OR "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchSource
	for rows.Next() {
		src, err := scanSearchSource(rows)
		if err != nil {
			return nil, err
		}
		if want[MessageKey{TenantID: src.TenantID, RoomID: src.RoomID, Seq: src.Seq}] {
			out = append(out, src)
		}
	}
	return out, rows.Err()
}

// ScanSearchSources reads one batch of rows after cursor in primary-key
// order, the walk ReindexSearchText uses.
func (s *MySQLChat) ScanSearchSources(ctx context.Context, cursor string, batch int) ([]SearchSource, SearchReindexBatch, error) {
	from, err := parseSearchCursor(cursor)
	if err != nil {
		return nil, SearchReindexBatch{}, err
	}
	batch = reindexBatchSize(batch)
	where, args := from.keysetAfter("m.")
	rows, err := s.DB.QueryContext(ctx, searchSourceSelect+where+` ORDER BY m.room_id, m.seq LIMIT ?`, append(args, batch)...)
	if err != nil {
		return nil, SearchReindexBatch{}, err
	}
	defer rows.Close()
	out := SearchReindexBatch{Cursor: cursor}
	var srcs []SearchSource
	for rows.Next() {
		src, err := scanSearchSource(rows)
		if err != nil {
			return nil, SearchReindexBatch{}, err
		}
		srcs = append(srcs, src)
		out.Cursor = searchCursor{src.RoomID, src.Seq}.String()
	}
	if err := rows.Err(); err != nil {
		return nil, SearchReindexBatch{}, err
	}
	out.Scanned = len(srcs)
	out.Done = out.Scanned < batch
	return srcs, out, nil
}
