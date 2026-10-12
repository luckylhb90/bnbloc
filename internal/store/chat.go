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
	"sync"

	"github.com/go-sql-driver/mysql"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// ErrDuplicateClientMsg reports that a message with the same client message id
// is already stored in the room (a concurrent or retried send won the race).
// The caller resolves the original sequence via LookupClientMsg and returns it
// instead of publishing a second copy.
var ErrDuplicateClientMsg = errors.New("store: duplicate client message id")

// ErrDuplicateSeq reports that the (room, seq) slot is already taken: the
// caller's sequence counter is behind the durable feed and must be re-seeded.
// Never silently ignored — a swallowed duplicate would deliver a message live
// that history never holds.
var ErrDuplicateSeq = errors.New("store: duplicate room message sequence")

// ClientMsgConflict reports the rows of a batch whose client message id is
// already stored in their room under their tenant (a concurrent or retried
// send won the race). Nothing of the batch was stored. It matches
// ErrDuplicateClientMsg under errors.Is, and carries each conflicting id's
// stored sequence so a caller answers the original without a second lookup.
type ClientMsgConflict struct {
	// Seqs maps each conflicting client message id to its stored sequence.
	Seqs map[string]int64
}

func (e *ClientMsgConflict) Error() string {
	return fmt.Sprintf("%v (%d of the batch)", ErrDuplicateClientMsg, len(e.Seqs))
}

// Is makes a conflict match ErrDuplicateClientMsg.
func (e *ClientMsgConflict) Is(target error) bool { return target == ErrDuplicateClientMsg }

// RoomMessage is one persisted room-feed entry (a chat_message or an
// agent_messages reply), stored so a client can page history beyond the
// realtime in-memory replay buffer.
type RoomMessage struct {
	// TenantID is the tenant a row is written under by AppendRoomMessages
	// (each publisher of one batch asserts its own). Reads and
	// AppendRoomMessage take the tenant from the context and leave it empty.
	TenantID string
	RoomID   string
	// ChannelID is the in-room channel this message belongs to; "" addresses
	// the empty/default channel (all rows written before channels existed).
	ChannelID   string
	Seq         int64
	Kind        string
	Author      string
	ClientMsgID string // optional client-generated id for send idempotency
	Payload     json.RawMessage
	CreatedAtMs int64
	// EditedAtMs is set when the author edited the message text; nil if never
	// edited. Surfaced to clients as `editedAtMs` so the feed can mark an edit.
	EditedAtMs *int64
}

type ChatStore interface {
	// AppendRoomMessage persists one message under the context's tenant: it
	// is AppendRoomMessages with a single row.
	AppendRoomMessage(ctx context.Context, m RoomMessage) error
	// AppendRoomMessages persists messages of ONE room atomically: every row
	// or none. Each row is written under its own TenantID (required). A
	// client message id already stored in the room under the row's tenant
	// returns *ClientMsgConflict (errors.Is ErrDuplicateClientMsg) naming
	// every such row; otherwise a taken (room, seq) returns ErrDuplicateSeq.
	// Rows of other rooms, or two rows with one client message id, are a
	// caller error.
	AppendRoomMessages(ctx context.Context, rows []RoomMessage) error
	// RoomHistory returns up to limit messages with seq < before (before<=0 means
	// "latest"), ascending by seq, within one channel. It is the page older than
	// a known cursor. channelID "" addresses the room's empty/default channel;
	// it is not a room-wide wildcard.
	RoomHistory(ctx context.Context, roomID, channelID string, before int64, limit int) ([]RoomMessage, error)
	// RoomFeed returns up to limit messages with seq > after across EVERY
	// channel of the room, ascending by seq, excluding soft-deleted rows. It is
	// the forward cursor the handoff export streams the whole feed with.
	RoomFeed(ctx context.Context, roomID string, after int64, limit int) ([]RoomMessage, error)
	// MarkRead advances a user's last-read sequence (monotonic), tracked per
	// (room, channel, user). channelID "" addresses the empty/default channel.
	MarkRead(ctx context.Context, roomID, channelID, userID string, seq, atMs int64) error
	// UnreadCount returns how many stored messages in a room's channel have seq
	// greater than the user's last-read sequence for that channel, at most
	// MaxUnreadCount (clients show "99+" beyond 99; counting a months-old
	// backlog in full on every room-list refresh would scan it all). channelID
	// "" addresses the empty/default channel.
	UnreadCount(ctx context.Context, roomID, channelID, userID string) (int, error)
	// MaxRoomSeq returns the highest stored sequence for a room, or 0 if none.
	// Sequences are a single room-scoped monotonic counter, so channelID "" is
	// room-wide (across every channel) — seq allocation must pass "". A
	// non-empty channelID restricts the max to that channel.
	MaxRoomSeq(ctx context.Context, roomID, channelID string) (int64, error)
	// LookupClientMsg returns the sequence of a previously-stored message with
	// this client-generated id in the room, for send idempotency. found=false
	// when there is none (or clientMsgID is empty).
	LookupClientMsg(ctx context.Context, roomID, clientMsgID string) (seq int64, found bool)
	// DeleteRoomMessage soft-deletes a message if requester is its author.
	// Returns deleted=false when the message is missing or not the requester's.
	DeleteRoomMessage(ctx context.Context, roomID string, seq int64, requester string, atMs int64) (deleted bool, err error)
	// EditRoomMessage updates a message's text and stamps edited_at_ms, but only
	// when requester is its author and it is not soft-deleted. Returns
	// edited=false when missing, not the requester's, or already deleted.
	EditRoomMessage(ctx context.Context, roomID string, seq int64, requester, text string, atMs int64) (edited bool, err error)
	// ChatIndex: single-message lookup and cross-room search (search.go).
	ChatIndex
}

// MemoryChat is an in-memory ChatStore for local dev / tests.
type MemoryChat struct {
	mu      sync.Mutex
	msgs    map[string][]RoomMessage // roomID -> ascending by seq
	read    map[string]int64         // "roomID\x00channelID\x00userID" -> last-read seq
	byCMI   map[string]int64         // "roomID\x00clientMsgID" -> seq
	deleted map[string]bool          // "roomID\x00seq" -> soft-deleted
}

// NewMemoryChat constructs an empty in-memory chat store.
func NewMemoryChat() *MemoryChat {
	return &MemoryChat{
		msgs:    map[string][]RoomMessage{},
		read:    map[string]int64{},
		byCMI:   map[string]int64{},
		deleted: map[string]bool{},
	}
}

// AppendRoomMessage stores one message under the context's tenant (see
// ChatStore).
func (s *MemoryChat) AppendRoomMessage(ctx context.Context, m RoomMessage) error {
	return appendOne(ctx, s, m)
}

// AppendRoomMessages stores one room's rows atomically: all are checked
// under the lock before any is written (see ChatStore).
func (s *MemoryChat) AppendRoomMessages(_ context.Context, rows []RoomMessage) error {
	if err := validateBatch(rows); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conflict := map[string]int64{}
	seqTaken := false
	for _, m := range rows {
		rk := m.TenantID + "\x00" + m.RoomID
		if m.ClientMsgID != "" {
			if seq, taken := s.byCMI[rk+"\x00"+m.ClientMsgID]; taken {
				conflict[m.ClientMsgID] = seq
			}
		}
		for _, e := range s.msgs[rk] {
			if e.Seq == m.Seq {
				seqTaken = true
			}
		}
	}
	if len(conflict) > 0 {
		return &ClientMsgConflict{Seqs: conflict}
	}
	if seqTaken {
		return ErrDuplicateSeq
	}
	for _, m := range rows {
		rk := m.TenantID + "\x00" + m.RoomID
		m.TenantID = "" // reads leave it empty (see RoomMessage)
		s.msgs[rk] = append(s.msgs[rk], m)
		if m.ClientMsgID != "" {
			s.byCMI[rk+"\x00"+m.ClientMsgID] = m.Seq
		}
	}
	return nil
}

// appendOne is AppendRoomMessage for every ChatStore: one row under the
// context's tenant.
func appendOne(ctx context.Context, s interface {
	AppendRoomMessages(context.Context, []RoomMessage) error
}, m RoomMessage) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.TenantID = tid
	return s.AppendRoomMessages(ctx, []RoomMessage{m})
}

// validateBatch enforces the AppendRoomMessages input contract: one room, a
// tenant on every row, and no client message id twice.
func validateBatch(rows []RoomMessage) error {
	seen := map[string]bool{}
	for _, m := range rows {
		if m.TenantID == "" {
			return tenant.ErrNoTenant
		}
		if m.RoomID != rows[0].RoomID {
			return fmt.Errorf("store: append batch spans rooms %q and %q", rows[0].RoomID, m.RoomID)
		}
		if m.ClientMsgID != "" {
			if seen[m.ClientMsgID] {
				return fmt.Errorf("store: append batch repeats client message id %q", m.ClientMsgID)
			}
			seen[m.ClientMsgID] = true
		}
	}
	return nil
}

// LookupClientMsg returns the seq stored under a client message id, if any.
func (s *MemoryChat) LookupClientMsg(ctx context.Context, roomID, clientMsgID string) (int64, bool) {
	if clientMsgID == "" {
		return 0, false
	}
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, ok := s.byCMI[tid+"\x00"+roomID+"\x00"+clientMsgID]
	return seq, ok
}

// DeleteRoomMessage soft-deletes a message if the requester authored it.
func (s *MemoryChat) DeleteRoomMessage(ctx context.Context, roomID string, seq int64, requester string, _ int64) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tid + "\x00" + roomID
	for _, m := range s.msgs[rk] {
		if m.Seq == seq {
			if m.Author != requester {
				return false, nil
			}
			s.deleted[rk+"\x00"+strconv.FormatInt(seq, 10)] = true
			return true, nil
		}
	}
	return false, nil
}

// EditRoomMessage updates a message's text and stamps EditedAtMs when the
// requester authored it and it is not soft-deleted. It mirrors the MySQL
// author/soft-delete guards so both stores behave identically.
func (s *MemoryChat) EditRoomMessage(ctx context.Context, roomID string, seq int64, requester, text string, atMs int64) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tid + "\x00" + roomID
	for i := range s.msgs[rk] {
		m := &s.msgs[rk][i]
		if m.Seq != seq {
			continue
		}
		if m.Author != requester {
			return false, nil
		}
		if s.deleted[rk+"\x00"+strconv.FormatInt(seq, 10)] {
			return false, nil
		}
		m.Payload = setPayloadText(m.Payload, text)
		at := atMs
		m.EditedAtMs = &at
		return true, nil
	}
	return false, nil
}

// setPayloadText returns the payload with its "text" field replaced, preserving
// other fields (e.g. userId, recipient). It matches how a chat message payload
// is stored so an edit rewrites the same field the feed renders.
func setPayloadText(payload json.RawMessage, text string) json.RawMessage {
	obj := map[string]any{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &obj)
	}
	obj["text"] = text
	b, err := json.Marshal(obj)
	if err != nil {
		return payload
	}
	return json.RawMessage(b)
}

// RoomHistory returns the page of messages older than before in one channel,
// ascending by seq.
func (s *MemoryChat) RoomHistory(ctx context.Context, roomID, channelID string, before int64, limit int) ([]RoomMessage, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tid + "\x00" + roomID
	all := s.msgs[rk]
	// `limit` so we return the newest page below the cursor, ascending. Deleted
	// messages are excluded.
	var matched []RoomMessage
	for _, m := range all {
		if m.ChannelID != channelID {
			continue
		}
		if s.deleted[rk+"\x00"+strconv.FormatInt(m.Seq, 10)] {
			continue
		}
		if before <= 0 || m.Seq < before {
			matched = append(matched, m)
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	return matched, nil
}

// RoomFeed returns the page of messages after the cursor across all channels,
// ascending by seq, excluding soft-deleted rows.
func (s *MemoryChat) RoomFeed(ctx context.Context, roomID string, after int64, limit int) ([]RoomMessage, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tid + "\x00" + roomID
	var matched []RoomMessage
	for _, m := range s.msgs[rk] {
		if m.Seq <= after || s.deleted[rk+"\x00"+strconv.FormatInt(m.Seq, 10)] {
			continue
		}
		matched = append(matched, m)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Seq < matched[j].Seq })
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

// MarkRead advances the user's last-read sequence (monotonic), per channel.
func (s *MemoryChat) MarkRead(ctx context.Context, roomID, channelID, userID string, seq, _ int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := tid + "\x00" + roomID + "\x00" + channelID + "\x00" + userID
	if seq > s.read[key] {
		s.read[key] = seq
	}
	return nil
}

// MaxUnreadCount bounds UnreadCount: one more than the largest number a client
// shows before "99+".
const MaxUnreadCount = 100

// UnreadCount counts stored messages in one channel with seq greater than the
// user's last-read sequence for that channel, excluding soft-deleted messages,
// up to MaxUnreadCount.
func (s *MemoryChat) UnreadCount(ctx context.Context, roomID, channelID, userID string) (int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tid + "\x00" + roomID
	lastRead := s.read[rk+"\x00"+channelID+"\x00"+userID]
	n := 0
	for _, m := range s.msgs[rk] {
		if m.ChannelID != channelID {
			continue
		}
		if s.deleted[rk+"\x00"+strconv.FormatInt(m.Seq, 10)] {
			continue
		}
		if m.Seq > lastRead {
			n++
			if n == MaxUnreadCount {
				break
			}
		}
	}
	return n, nil
}

// MaxRoomSeq returns the highest stored sequence for a room, or 0 if none.
// channelID "" is room-wide (seq is one room-scoped counter, so allocation
// reads ""); a non-empty channelID restricts the max to that channel.
func (s *MemoryChat) MaxRoomSeq(ctx context.Context, roomID, channelID string) (int64, error) {
	tid, err := tenant.Require(ctx)
	// SeedSeq callers may not yet have a request tenant, but sequence allocation
	// is room-wide and room ids are globally unique in the room table. Permit only
	// the documented room-wide lookup (channelID == "") in that tenant-less path.
	if err != nil && channelID != "" {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var mx int64
	for key, msgs := range s.msgs {
		if err == nil && key != tid+"\x00"+roomID {
			continue
		}
		if err != nil && !strings.HasSuffix(key, "\x00"+roomID) {
			continue
		}
		for _, m := range msgs {
			if channelID != "" && m.ChannelID != channelID {
				continue
			}
			if m.Seq > mx {
				mx = m.Seq
			}
		}
	}
	return mx, nil
}

// MySQLChat implements ChatStore over database/sql.
type MySQLChat struct{ DB *sql.DB }

// NewMySQLChat constructs a MySQL-backed chat store.
func NewMySQLChat(db *sql.DB) *MySQLChat { return &MySQLChat{DB: db} }

var _ ChatStore = (*MySQLChat)(nil)
var _ ChatStore = (*MemoryChat)(nil)

// AppendRoomMessage inserts one message under the context's tenant (see
// ChatStore).
func (s *MySQLChat) AppendRoomMessage(ctx context.Context, m RoomMessage) error {
	return appendOne(ctx, s, m)
}

// AppendRoomMessages inserts one room's rows in ONE multi-row INSERT: a
// single statement is atomic in InnoDB, so the batch costs one round trip
// and one commit, and a failure stores nothing. A unique-key violation is
// classified, never swallowed: by data, not by index name — rows whose
// client id now resolves in their tenant lost an idempotency race
// (*ClientMsgConflict); otherwise the (room, seq) slot is taken
// (ErrDuplicateSeq).
func (s *MySQLChat) AppendRoomMessages(ctx context.Context, rows []RoomMessage) error {
	if len(rows) == 0 {
		return nil
	}
	if err := validateBatch(rows); err != nil {
		return err
	}
	var q strings.Builder
	q.WriteString(`INSERT INTO room_messages (tenant_id, room_id, channel_id, seq, kind, author, client_msg_id, payload_json, search_text, created_at_ms) VALUES `)
	args := make([]any, 0, len(rows)*10)
	for i, m := range rows {
		if i > 0 {
			q.WriteString(", ")
		}
		q.WriteString("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		payload := m.Payload
		if len(payload) == 0 {
			payload = json.RawMessage("{}")
		}
		args = append(args, m.TenantID, m.RoomID, m.ChannelID, m.Seq, m.Kind, nullStr(m.Author), nullStr(m.ClientMsgID),
			payload, nullStr(MessageText(m.Kind, payload)), m.CreatedAtMs)
	}
	_, err := s.DB.ExecContext(ctx, q.String(), args...)
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		conflict, cerr := s.clientMsgConflicts(ctx, rows)
		if cerr != nil {
			return fmt.Errorf("classify duplicate: %w (insert: %v)", cerr, err)
		}
		if len(conflict) > 0 {
			return &ClientMsgConflict{Seqs: conflict}
		}
		return ErrDuplicateSeq
	}
	return err
}

// clientMsgConflicts returns, for the rows carrying a client id, the stored
// sequence of each id already present in the room under the row's tenant.
func (s *MySQLChat) clientMsgConflicts(ctx context.Context, rows []RoomMessage) (map[string]int64, error) {
	tenantOf := map[string]string{}
	args := []any{rows[0].RoomID}
	for _, m := range rows {
		if m.ClientMsgID != "" {
			tenantOf[m.ClientMsgID] = m.TenantID
			args = append(args, m.ClientMsgID)
		}
	}
	out := map[string]int64{}
	if len(tenantOf) == 0 {
		return out, nil
	}
	res, err := s.DB.QueryContext(ctx,
		`SELECT tenant_id, client_msg_id, seq FROM room_messages WHERE room_id = ? AND client_msg_id IN (?`+
			strings.Repeat(", ?", len(tenantOf)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer res.Close()
	for res.Next() {
		var tid, cmi string
		var seq int64
		if err := res.Scan(&tid, &cmi, &seq); err != nil {
			return nil, err
		}
		if tenantOf[cmi] == tid {
			out[cmi] = seq
		}
	}
	return out, res.Err()
}

// LookupClientMsg returns the seq of a stored message with this client id.
func (s *MySQLChat) LookupClientMsg(ctx context.Context, roomID, clientMsgID string) (int64, bool) {
	if clientMsgID == "" {
		return 0, false
	}
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, false
	}
	var seq int64
	err = s.DB.QueryRowContext(ctx,
		`SELECT seq FROM room_messages WHERE tenant_id = ? AND room_id = ? AND client_msg_id = ?`,
		tid, roomID, clientMsgID).Scan(&seq)
	if err != nil {
		return 0, false
	}
	return seq, true
}

// RoomHistory returns up to limit messages with seq < before (before<=0 =
// latest) in one channel, ascending by seq. It selects the newest page below
// the cursor via a descending limit, then reverses to ascending order for the
// client.
func (s *MySQLChat) RoomHistory(ctx context.Context, roomID, channelID string, before int64, limit int) ([]RoomMessage, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	if before > 0 {
		rows, err = s.DB.QueryContext(ctx,
			`SELECT `+roomMessageCols+`
			 FROM room_messages WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND seq < ? AND deleted_at_ms IS NULL
			 ORDER BY seq DESC LIMIT ?`, tid, roomID, channelID, before, limit)
	} else {
		rows, err = s.DB.QueryContext(ctx,
			`SELECT `+roomMessageCols+`
			 FROM room_messages WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND deleted_at_ms IS NULL
			 ORDER BY seq DESC LIMIT ?`, tid, roomID, channelID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var desc []RoomMessage
	for rows.Next() {
		m, err := scanRoomMessage(rows)
		if err != nil {
			return nil, err
		}
		desc = append(desc, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to ascending by seq.
	for i, j := 0, len(desc)-1; i < j; i, j = i+1, j-1 {
		desc[i], desc[j] = desc[j], desc[i]
	}
	return desc, nil
}

// RoomFeed returns up to limit messages with seq > after across every channel
// of the room, ascending by seq, excluding soft-deleted rows.
func (s *MySQLChat) RoomFeed(ctx context.Context, roomID string, after int64, limit int) ([]RoomMessage, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT `+roomMessageCols+`
		 FROM room_messages WHERE tenant_id = ? AND room_id = ? AND seq > ? AND deleted_at_ms IS NULL
		 ORDER BY seq ASC LIMIT ?`, tid, roomID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoomMessage
	for rows.Next() {
		m, err := scanRoomMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkRead advances the user's last-read sequence in a channel (monotonic via
// GREATEST). The row is keyed by the table's (room, channel, user) primary key.
func (s *MySQLChat) MarkRead(ctx context.Context, roomID, channelID, userID string, seq, atMs int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO room_read_state (tenant_id, room_id, channel_id, user_id, last_read_seq, updated_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		  last_read_seq = GREATEST(last_read_seq, VALUES(last_read_seq)),
		  updated_at_ms = VALUES(updated_at_ms)`,
		tid, roomID, channelID, userID, seq, atMs)
	return err
}

// UnreadCount counts messages in one channel with seq greater than the user's
// last-read sequence for that channel, up to MaxUnreadCount: the inner LIMIT
// stops the range scan of idx_room_channel_seq (and the clustered reads the
// deleted_at_ms filter needs) after MaxUnreadCount rows.
func (s *MySQLChat) UnreadCount(ctx context.Context, roomID, channelID, userID string) (int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	var lastRead int64
	err = s.DB.QueryRowContext(ctx,
		`SELECT COALESCE(last_read_seq, 0) FROM room_read_state
		 WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND user_id = ?`, tid, roomID, channelID, userID).Scan(&lastRead)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	var n int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (SELECT 1 FROM room_messages
		 WHERE tenant_id = ? AND room_id = ? AND channel_id = ? AND seq > ? AND deleted_at_ms IS NULL
		 LIMIT ?) unread`,
		tid, roomID, channelID, lastRead, MaxUnreadCount).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// MaxRoomSeq returns the highest stored sequence for a room, or 0 if none.
// channelID "" is room-wide (seq is one room-scoped counter, so allocation
// reads ""); a non-empty channelID restricts the max to that channel.
func (s *MySQLChat) MaxRoomSeq(ctx context.Context, roomID, channelID string) (int64, error) {
	tid, terr := tenant.Require(ctx)
	// SeedSeq callers may not yet have a request tenant, but sequence allocation
	// is room-wide and room ids are globally unique in the room table. Permit only
	// the documented room-wide lookup (channelID == "") in that tenant-less path.
	q := `SELECT MAX(seq) FROM room_messages WHERE room_id = ?`
	args := []any{roomID}
	if terr == nil {
		q = `SELECT MAX(seq) FROM room_messages WHERE tenant_id = ? AND room_id = ?`
		args = []any{tid, roomID}
	} else if channelID != "" {
		return 0, terr
	}
	if channelID != "" {
		q += ` AND channel_id = ?`
		args = append(args, channelID)
	}
	var mx sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, q, args...).Scan(&mx); err != nil {
		return 0, err
	}
	if mx.Valid {
		return mx.Int64, nil
	}
	return 0, nil
}

// DeleteRoomMessage soft-deletes a message when the requester authored it, by
// setting deleted_at_ms. The author check is enforced in the WHERE clause. It is
// idempotent: deleting an already-deleted message the requester authored still
// returns deleted=true, matching MemoryChat. A missing message or a non-author
// requester returns deleted=false.
func (s *MySQLChat) DeleteRoomMessage(ctx context.Context, roomID string, seq int64, requester string, atMs int64) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE room_messages SET deleted_at_ms = ?
		 WHERE tenant_id = ? AND room_id = ? AND seq = ? AND author = ? AND deleted_at_ms IS NULL`,
		atMs, tid, roomID, seq, requester)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	// 0 rows affected: either the message is missing / not the requester's, or it
	// was already deleted by this author. Distinguish so a repeated delete by the
	// author is idempotent (true), matching MemoryChat.
	var one int
	err = s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM room_messages
		 WHERE tenant_id = ? AND room_id = ? AND seq = ? AND author = ? AND deleted_at_ms IS NOT NULL LIMIT 1`,
		tid, roomID, seq, requester).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// EditRoomMessage rewrites a message's text and stamps edited_at_ms, but only
// when the requester authored it and it is not soft-deleted (both enforced in
// the WHERE clause). Returns edited=false when 0 rows match. It rewrites the
// payload's "text" field via JSON_SET so the stored payload the feed renders is
// updated in place, preserving other payload fields.
func (s *MySQLChat) EditRoomMessage(ctx context.Context, roomID string, seq int64, requester, text string, atMs int64) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE room_messages
		 SET payload_json = JSON_SET(payload_json, '$.text', ?), search_text = ?, edited_at_ms = ?
		 WHERE tenant_id = ? AND room_id = ? AND seq = ? AND author = ? AND deleted_at_ms IS NULL`,
		text, text, atMs, tid, roomID, seq, requester)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
