package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The reindex walk (search_source.go, ScanSearchSources): an external
// search index is brought up to date for one bounded batch of rows at a
// time, walking room_messages in primary-key order ((room_id, seq)) from an
// opaque cursor, so a caller pages through the whole feed and resumes where
// it stopped. It is cross-tenant by design: a platform operation over the
// whole feed. The store's own FULLTEXT index needs no walk: search_text is
// written with the row.

// SearchReindexBatchSize is the number of rows one batch reads.
const SearchReindexBatchSize = 500

// SearchReindexBatch reports one batch: rows read, rows (documents) written,
// the cursor to continue from, and whether the end of the table was
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
	return clampLimit(n, SearchReindexBatchSize, SearchReindexBatchSize)
}
