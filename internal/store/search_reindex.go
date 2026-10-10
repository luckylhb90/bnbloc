package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Search backfill (batch 3, R7). Messages persisted before migration
// 2026_10_message_search carry no room_messages.search_text, so the FULLTEXT
// search never finds them. ReindexSearchText fills it for one bounded batch of
// rows, walking the table in primary-key order ((room_id, seq)) from an opaque
// cursor, so a caller can page through the whole feed in bounded steps and
// resume where it stopped.
//
// It is idempotent and safe while serving: the text comes from MessageText
// (the exact normalization AppendRoomMessage writes), and each row is only
// written while its search_text is still empty, so a concurrent edit (which
// rewrites search_text) or a second reindex run never clobbers newer text.
// It is cross-tenant by design: a platform operation over the whole feed.

// SearchReindexBatchSize is the number of rows one batch reads.
const SearchReindexBatchSize = 500

// SearchReindexBatch reports one batch: rows read, rows whose search text was
// filled, the cursor to continue from, and whether the end of the table was
// reached.
type SearchReindexBatch struct {
	Scanned int    `json:"scanned"`
	Updated int    `json:"updated"`
	Cursor  string `json:"cursor"`
	Done    bool   `json:"done"`
}

// searchCursor is the (room_id, seq) of the last row a batch read. The empty
// cursor starts before the first row.
type searchCursor struct {
	roomID string
	seq    int64
}

// String encodes the cursor as "<roomID>/<seq>" (split on the last slash).
func (c searchCursor) String() string {
	if c.roomID == "" && c.seq == 0 {
		return ""
	}
	return c.roomID + "/" + strconv.FormatInt(c.seq, 10)
}

// ErrInvalidSearchCursor is a reindex cursor this store did not issue.
var ErrInvalidSearchCursor = errors.New("invalid reindex cursor")

func parseSearchCursor(s string) (searchCursor, error) {
	if s == "" {
		return searchCursor{}, nil
	}
	i := strings.LastIndex(s, "/")
	if i <= 0 {
		return searchCursor{}, fmt.Errorf("%w %q", ErrInvalidSearchCursor, s)
	}
	seq, err := strconv.ParseInt(s[i+1:], 10, 64)
	if err != nil {
		return searchCursor{}, fmt.Errorf("%w %q", ErrInvalidSearchCursor, s)
	}
	return searchCursor{roomID: s[:i], seq: seq}, nil
}

// keysetAfter is the WHERE clause (with its leading space; "" for the empty
// cursor) selecting the rows after c in primary-key order, columns qualified
// by prefix. It is spelled out rather than (room_id, seq) > (?, ?): MySQL
// turns this form into a primary-key range scan, the row comparison into a
// full index scan from the first row.
func (c searchCursor) keysetAfter(prefix string) (string, []any) {
	if c.roomID == "" && c.seq == 0 {
		return "", nil
	}
	return " WHERE " + prefix + "room_id >= ? AND (" + prefix + "room_id > ? OR " + prefix + "seq > ?)",
		[]any{c.roomID, c.roomID, c.seq}
}

func (c searchCursor) less(roomID string, seq int64) bool {
	return c.roomID < roomID || (c.roomID == roomID && c.seq < seq)
}

func reindexBatchSize(n int) int {
	if n <= 0 || n > SearchReindexBatchSize {
		return SearchReindexBatchSize
	}
	return n
}

// ReindexSearchText for the memory backend: search matches the payload text
// on every query, so there is never stale index text to fill. It still walks
// the rows with the same cursor contract (Updated is always 0).
func (s *MemoryChat) ReindexSearchText(_ context.Context, cursor string, batch int) (SearchReindexBatch, error) {
	from, err := parseSearchCursor(cursor)
	if err != nil {
		return SearchReindexBatch{}, err
	}
	batch = reindexBatchSize(batch)
	s.mu.Lock()
	var rows []searchCursor
	for key, msgs := range s.msgs {
		roomID := key[strings.LastIndex(key, "\x00")+1:]
		for _, m := range msgs {
			if from.less(roomID, m.Seq) {
				rows = append(rows, searchCursor{roomID: roomID, seq: m.Seq})
			}
		}
	}
	s.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].less(rows[j].roomID, rows[j].seq) })
	// Done when the batch is short, the same rule as the MySQL backend.
	out := SearchReindexBatch{Cursor: cursor, Done: len(rows) < batch}
	if len(rows) > batch {
		rows = rows[:batch]
	}
	out.Scanned = len(rows)
	if len(rows) > 0 {
		out.Cursor = rows[len(rows)-1].String()
	}
	return out, nil
}

// ReindexSearchText fills room_messages.search_text for one batch of rows
// after cursor. The payload is only read for rows that need indexing.
func (s *MySQLChat) ReindexSearchText(ctx context.Context, cursor string, batch int) (SearchReindexBatch, error) {
	from, err := parseSearchCursor(cursor)
	if err != nil {
		return SearchReindexBatch{}, err
	}
	batch = reindexBatchSize(batch)
	where, args := from.keysetAfter("")
	q := `SELECT room_id, seq, kind,
	        CASE WHEN (search_text IS NULL OR search_text = '') AND kind IN ('chat_message', 'agent_messages')
	             THEN payload_json END
	      FROM room_messages` + where + ` ORDER BY room_id, seq LIMIT ?`
	args = append(args, batch)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return SearchReindexBatch{}, err
	}
	type pending struct {
		at   searchCursor
		text string
	}
	var todo []pending
	out := SearchReindexBatch{Cursor: cursor}
	for rows.Next() {
		var at searchCursor
		var kind string
		var payload sql.RawBytes
		if err := rows.Scan(&at.roomID, &at.seq, &kind, &payload); err != nil {
			rows.Close()
			return SearchReindexBatch{}, err
		}
		out.Scanned++
		out.Cursor = at.String()
		if payload != nil {
			if text := MessageText(kind, json.RawMessage(payload)); text != "" {
				todo = append(todo, pending{at: at, text: text})
			}
		}
	}
	if err := rows.Close(); err != nil {
		return SearchReindexBatch{}, err
	}
	if err := rows.Err(); err != nil {
		return SearchReindexBatch{}, err
	}
	out.Done = out.Scanned < batch
	for _, p := range todo {
		res, err := s.DB.ExecContext(ctx,
			`UPDATE room_messages SET search_text = ?
			 WHERE room_id = ? AND seq = ? AND (search_text IS NULL OR search_text = '')`,
			p.text, p.at.roomID, p.at.seq)
		if err != nil {
			return SearchReindexBatch{}, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out.Updated++
		}
	}
	return out, nil
}
