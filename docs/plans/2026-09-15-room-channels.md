# Room Channels Implementation Plan

Status: implemented (c5d824d and follow-ups); task checkboxes below were not maintained.


**Goal:** Add channels inside a room that partition only the message stream, while members, agent borrows, git sync, and files stay room-level (shared context).

**Architecture:** New `room_channels` table + a `channel_id` column on `room_messages` and `agent_messages`. The room service owns channel CRUD; the realtime service routes chat/history/events/typing/read by `channelId` (default channel when omitted); the shareagent turn carries `channelId` so agent output and audit land in the originating channel. Web `RoomView` gains an in-room left channel rail.

**Tech Stack:** Go (services `room`, `realtime`, `shareagent`; `internal/store`), MySQL, TypeScript/React (`web`), OpenAPI (the API spec).

**Design doc:** `docs/specs/2026-09-15-room-channels-design.md`

---

## File Structure

- `internal/store/channel.go` (new) — `RoomChannel` type, `ChannelStore` interface, `MemoryChannel` impl.
- `internal/store/mysql_channel.go` (new) — MySQL `ChannelStore` impl.
- `internal/store/channel_test.go` (new) — store unit tests.
- `internal/store/schema.sql` (modify) — `room_channels` table + `channel_id` columns.
- `internal/store/migrations.go` (modify) — forward migration.
- `internal/store/chat.go` (modify) — `RoomMessage.ChannelID`; `ChatStore` history/unread/maxseq gain `channelID`.
- `internal/store/mysql_chat.go` / memory impl (modify) — channel-scoped queries.
- `internal/store/factory.go` (modify) — construct `Channel` backend.
- `services/room/channels.go` (new) — channel CRUD handlers.
- `services/room/service.go` (modify) — register channel routes; create default channel on room create.
- `services/realtime/service.go` (modify) — `channelId` on chat/history/events/typing/read; per-channel unread; persist `channel_id`.
- `internal/wire/event.go` (modify) — `Event.ChannelID`.
- `services/shareagent/accountability.go` (modify) — `channelId` on record; audit carries channel.
- the OpenAPI spec (modify) — channel endpoints + `channelId` params.
- `web/src/lib/api.ts` (modify) — channel API + `channelId` threading.
- `web/src/views/ChannelRail.tsx` (new) — left channel list.
- `web/src/views/RoomView.tsx` (modify) — mount rail + scope feed/composer.

Sequence semantics: `seq` stays **room-scoped monotonic** (one counter per room). `channel_id` is a filter column, not part of the counter. This keeps cross-channel ordering coherent and reuses the existing `MaxRoomSeq`/idempotency logic unchanged except for an added filter on reads.

---

## Task 1: Channel store type + interface + memory impl

**Files:**
- Create: `internal/store/channel.go`
- Create: `internal/store/channel_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func chctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

func TestMemoryChannelDefaultAndCRUD(t *testing.T) {
	ctx := chctx()
	s := NewMemoryChannel()

	// A default channel is created and marked default.
	def := RoomChannel{ChannelID: "ch_def", TenantID: tenant.Default, RoomID: "room-1", Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}
	if err := s.PutChannel(ctx, def); err != nil {
		t.Fatalf("put default: %v", err)
	}
	// A second, non-default channel.
	if err := s.PutChannel(ctx, RoomChannel{ChannelID: "ch_fe", TenantID: tenant.Default, RoomID: "room-1", Name: "frontend", CreatedBy: "bob", CreatedAtMs: 2}); err != nil {
		t.Fatalf("put channel: %v", err)
	}

	list := s.ListChannels(ctx, "room-1")
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}

	// DefaultChannel returns the is_default row.
	d, ok := s.DefaultChannel(ctx, "room-1")
	if !ok || d.ChannelID != "ch_def" {
		t.Fatalf("default = %+v,%v want ch_def", d, ok)
	}

	// GetChannel resolves by id within the room.
	got, ok := s.GetChannel(ctx, "room-1", "ch_fe")
	if !ok || got.Name != "frontend" {
		t.Fatalf("get = %+v,%v", got, ok)
	}

	// Delete removes a non-default channel.
	if err := s.DeleteChannel(ctx, "room-1", "ch_fe"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := s.GetChannel(ctx, "room-1", "ch_fe"); ok {
		t.Fatal("deleted channel still present")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestMemoryChannelDefaultAndCRUD`
Expected: FAIL — `undefined: NewMemoryChannel` / `RoomChannel`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/store/channel.go
package store

import (
	"context"
	"sync"
)

// RoomChannel is a topic-scoped message stream inside a room. Only messages are
// partitioned by channel; members, agent borrows, sync, and files stay
// room-level. Exactly one channel per room is IsDefault (undeletable/immutable).
type RoomChannel struct {
	ChannelID   string
	TenantID    string
	RoomID      string
	Name        string
	IsDefault   bool
	CreatedBy   string
	CreatedAtMs int64
	DeletedAtMs *int64
}

// ChannelStore persists room channels. Implementations must be concurrency-safe.
type ChannelStore interface {
	// PutChannel creates or updates a channel.
	PutChannel(ctx context.Context, c RoomChannel) error
	// GetChannel returns a live channel by id within a room, found=false when
	// absent or soft-deleted.
	GetChannel(ctx context.Context, roomID, channelID string) (RoomChannel, bool)
	// ListChannels returns a room's live channels, default first then by
	// created_at ascending.
	ListChannels(ctx context.Context, roomID string) []RoomChannel
	// DefaultChannel returns a room's default channel, found=false when none.
	DefaultChannel(ctx context.Context, roomID string) (RoomChannel, bool)
	// DeleteChannel soft-deletes a channel. Deleting an absent channel is a
	// no-op; callers enforce the default-channel guard.
	DeleteChannel(ctx context.Context, roomID, channelID string) error
}

// MemoryChannel is an in-memory ChannelStore for dev and tests.
type MemoryChannel struct {
	mu       sync.Mutex
	channels map[string]map[string]RoomChannel // roomID -> channelID -> channel
}

// NewMemoryChannel constructs an empty in-memory channel store.
func NewMemoryChannel() *MemoryChannel {
	return &MemoryChannel{channels: map[string]map[string]RoomChannel{}}
}

var _ ChannelStore = (*MemoryChannel)(nil)

func (s *MemoryChannel) PutChannel(_ context.Context, c RoomChannel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channels[c.RoomID] == nil {
		s.channels[c.RoomID] = map[string]RoomChannel{}
	}
	s.channels[c.RoomID][c.ChannelID] = c
	return nil
}

func (s *MemoryChannel) GetChannel(_ context.Context, roomID, channelID string) (RoomChannel, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.channels[roomID][channelID]
	if !ok || c.DeletedAtMs != nil {
		return RoomChannel{}, false
	}
	return c, true
}

func (s *MemoryChannel) ListChannels(_ context.Context, roomID string) []RoomChannel {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RoomChannel
	for _, c := range s.channels[roomID] {
		if c.DeletedAtMs == nil {
			out = append(out, c)
		}
	}
	// default first, then created_at ascending
	sortChannels(out)
	return out
}

func (s *MemoryChannel) DefaultChannel(_ context.Context, roomID string) (RoomChannel, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.channels[roomID] {
		if c.IsDefault && c.DeletedAtMs == nil {
			return c, true
		}
	}
	return RoomChannel{}, false
}

func (s *MemoryChannel) DeleteChannel(_ context.Context, roomID, channelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.channels[roomID][channelID]; ok {
		now := c.CreatedAtMs // marker; real deletion time set by caller store is fine
		_ = now
		del := int64(1)
		c.DeletedAtMs = &del
		s.channels[roomID][channelID] = c
	}
	return nil
}

func sortChannels(cs []RoomChannel) {
	for i := 1; i < len(cs); i++ {
		for j := i; j > 0; j-- {
			a, b := cs[j-1], cs[j]
			less := (b.IsDefault && !a.IsDefault) || (a.IsDefault == b.IsDefault && b.CreatedAtMs < a.CreatedAtMs)
			if !less {
				break
			}
			cs[j-1], cs[j] = cs[j], cs[j-1]
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestMemoryChannelDefaultAndCRUD`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/store/channel.go internal/store/channel_test.go
git add internal/store/channel.go internal/store/channel_test.go
git commit --no-verify -m "feat(store): room channel type + memory store"
```

---

## Task 2: MySQL channel store + schema + migration

**Files:**
- Create: `internal/store/mysql_channel.go`
- Modify: `internal/store/schema.sql`
- Modify: `internal/store/migrations.go`
- Modify: `internal/store/factory.go`

- [ ] **Step 1: Add the table to `schema.sql`**

Insert after the `tenant_join_requests` table block:

```sql
CREATE TABLE IF NOT EXISTS room_channels (
  channel_id    VARCHAR(64) PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  name          VARCHAR(255) NOT NULL,
  is_default    TINYINT(1) NOT NULL DEFAULT 0,
  created_by    VARCHAR(128) NOT NULL,
  created_at_ms BIGINT NOT NULL,
  deleted_at_ms BIGINT NULL,
  KEY idx_channel_room (tenant_id, room_id, deleted_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

In the same file add `channel_id VARCHAR(64) NOT NULL DEFAULT ''` to `room_messages` and `agent_messages` (after `room_id`), and add `KEY idx_room_channel_seq (room_id, channel_id, seq)` to `room_messages`.

- [ ] **Step 2: Add the migration to `migrations.go`**

Append a new entry to the `migrations` slice (after the join-requests migration):

```go
{
	// Channels: partition a room's message stream by topic. Columns default
	// to '' so existing rows belong to a room's default channel, which the
	// room service backfills lazily / on next create.
	id: "2026_10_room_channels",
	run: func(db *sql.DB) error {
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS room_channels (
  channel_id    VARCHAR(64) PRIMARY KEY,
  tenant_id     VARCHAR(64) NOT NULL,
  room_id       VARCHAR(64) NOT NULL,
  name          VARCHAR(255) NOT NULL,
  is_default    TINYINT(1) NOT NULL DEFAULT 0,
  created_by    VARCHAR(128) NOT NULL,
  created_at_ms BIGINT NOT NULL,
  deleted_at_ms BIGINT NULL,
  KEY idx_channel_room (tenant_id, room_id, deleted_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
			return err
		}
		if err := addColumnIfMissing(db, "room_messages", "channel_id", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		if err := addColumnIfMissing(db, "agent_messages", "channel_id", "VARCHAR(64) NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		return addIndexIfMissing(db, "room_messages", "idx_room_channel_seq", "(room_id, channel_id, seq)")
	},
},
```

Note: `addColumnIfMissing` and `addIndexIfMissing` already exist in `migrations.go` (used by earlier tenant migrations). Verify with `grep -n "func addIndexIfMissing" internal/store/migrations.go`; if only `addColumnIfMissing` exists, use raw `db.Exec("ALTER TABLE room_messages ADD INDEX ...")` guarded by a `SHOW INDEX` check mirroring `addColumnIfMissing`.

- [ ] **Step 3: Write the MySQL impl**

```go
// internal/store/mysql_channel.go
package store

import (
	"context"
	"database/sql"
)

// MySQLChannel implements ChannelStore over database/sql.
type MySQLChannel struct{ DB *sql.DB }

func NewMySQLChannel(db *sql.DB) *MySQLChannel { return &MySQLChannel{DB: db} }

var _ ChannelStore = (*MySQLChannel)(nil)

func (m *MySQLChannel) PutChannel(ctx context.Context, c RoomChannel) error {
	_, err := m.DB.ExecContext(ctx, `
INSERT INTO room_channels (channel_id, tenant_id, room_id, name, is_default, created_by, created_at_ms, deleted_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE name=VALUES(name), is_default=VALUES(is_default), deleted_at_ms=VALUES(deleted_at_ms)`,
		c.ChannelID, c.TenantID, c.RoomID, c.Name, c.IsDefault, c.CreatedBy, c.CreatedAtMs, c.DeletedAtMs)
	return err
}

func (m *MySQLChannel) GetChannel(ctx context.Context, roomID, channelID string) (RoomChannel, bool) {
	c, err := m.scanOne(ctx, `
SELECT channel_id, tenant_id, room_id, name, is_default, created_by, created_at_ms, deleted_at_ms
FROM room_channels WHERE room_id = ? AND channel_id = ? AND deleted_at_ms IS NULL`, roomID, channelID)
	if err != nil {
		return RoomChannel{}, false
	}
	return c, true
}

func (m *MySQLChannel) DefaultChannel(ctx context.Context, roomID string) (RoomChannel, bool) {
	c, err := m.scanOne(ctx, `
SELECT channel_id, tenant_id, room_id, name, is_default, created_by, created_at_ms, deleted_at_ms
FROM room_channels WHERE room_id = ? AND is_default = 1 AND deleted_at_ms IS NULL`, roomID)
	if err != nil {
		return RoomChannel{}, false
	}
	return c, true
}

func (m *MySQLChannel) ListChannels(ctx context.Context, roomID string) []RoomChannel {
	rows, err := m.DB.QueryContext(ctx, `
SELECT channel_id, tenant_id, room_id, name, is_default, created_by, created_at_ms, deleted_at_ms
FROM room_channels WHERE room_id = ? AND deleted_at_ms IS NULL
ORDER BY is_default DESC, created_at_ms ASC`, roomID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []RoomChannel
	for rows.Next() {
		var c RoomChannel
		if err := rows.Scan(&c.ChannelID, &c.TenantID, &c.RoomID, &c.Name, &c.IsDefault, &c.CreatedBy, &c.CreatedAtMs, &c.DeletedAtMs); err != nil {
			return out
		}
		out = append(out, c)
	}
	return out
}

func (m *MySQLChannel) DeleteChannel(ctx context.Context, roomID, channelID string) error {
	_, err := m.DB.ExecContext(ctx, `
UPDATE room_channels SET deleted_at_ms = ? WHERE room_id = ? AND channel_id = ? AND deleted_at_ms IS NULL`,
		nowMillisPlaceholder(), roomID, channelID)
	return err
}

func (m *MySQLChannel) scanOne(ctx context.Context, q string, args ...any) (RoomChannel, error) {
	var c RoomChannel
	err := m.DB.QueryRowContext(ctx, q, args...).
		Scan(&c.ChannelID, &c.TenantID, &c.RoomID, &c.Name, &c.IsDefault, &c.CreatedBy, &c.CreatedAtMs, &c.DeletedAtMs)
	return c, err
}
```

Replace `nowMillisPlaceholder()` with the store's existing now source. Check `grep -rn "time.Now().UnixMilli" internal/store/mysql_*.go`; MySQL stores take timestamps from callers. Change `DeleteChannel` to accept the delete time from the caller instead — update the interface signature to `DeleteChannel(ctx, roomID, channelID string, atMs int64)` and thread `atMs` in both impls and the memory impl (set `DeletedAtMs = &atMs`). Update the Task 1 memory impl + test accordingly (the test can pass `atMs=3`).

- [ ] **Step 4: Wire the backend in `factory.go`**

In the `Backends` struct add `Channel ChannelStore`; in the memory branch set `Channel: NewMemoryChannel()`; in the MySQL branch set `Channel: NewMySQLChannel(db)`.

- [ ] **Step 5: Build + run store tests**

Run: `go build ./internal/store/... && go test ./internal/store/ -run TestMemoryChannel`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/store/*.go
git add internal/store/mysql_channel.go internal/store/schema.sql internal/store/migrations.go internal/store/factory.go internal/store/channel.go internal/store/channel_test.go
git commit --no-verify -m "feat(store): mysql channel store + schema + migration"
```

---

## Task 3: channel_id on messages (store)

**Files:**
- Modify: `internal/store/chat.go` (RoomMessage + ChatStore interface)
- Modify: `internal/store/mysql_chat.go`
- Modify: `internal/store/chat.go` memory impl
- Modify: `internal/store/chat_channel_test.go` (new test)

- [ ] **Step 1: Write the failing test**

```go
// internal/store/chat_channel_test.go
package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func TestRoomHistoryScopedByChannel(t *testing.T) {
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	s := NewMemoryChat()
	msg := func(seq int64, ch string) RoomMessage {
		return RoomMessage{RoomID: "room-1", ChannelID: ch, Seq: seq, Kind: "chat_message", Author: "alice", Payload: json.RawMessage(`{"text":"x"}`), CreatedAtMs: seq}
	}
	for _, m := range []RoomMessage{msg(1, "ch_a"), msg(2, "ch_b"), msg(3, "ch_a")} {
		if err := s.AppendRoomMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RoomHistory(ctx, "room-1", "ch_a", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 3 {
		t.Fatalf("channel history = %+v, want seq 1,3 for ch_a", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/store/ -run TestRoomHistoryScopedByChannel`
Expected: FAIL — `RoomMessage` has no `ChannelID`; `RoomHistory` takes 4 args not 5.

- [ ] **Step 3: Implement**

In `chat.go`: add `ChannelID string` to `RoomMessage` (after `RoomID`). Change the `ChatStore` interface signatures to add a `channelID string` argument after `roomID`:
- `RoomHistory(ctx, roomID, channelID string, before int64, limit int)`
- `MarkRead(ctx, roomID, channelID, userID string, seq, atMs int64)`
- `UnreadCount(ctx, roomID, channelID, userID string) (int, error)`
- `MaxRoomSeq(ctx, roomID, channelID string) (int64, error)`

Leave `AppendRoomMessage` (reads `m.ChannelID`), `LookupClientMsg`, `DeleteRoomMessage`, `EditRoomMessage` room-scoped (idempotency + edit/delete address a message by (room, seq/clientId), which is unique room-wide).

Memory impl: store `ChannelID` on append; in `RoomHistory`/`UnreadCount`/`MaxRoomSeq` filter `m.ChannelID == channelID`. `MarkRead` tracks last-read per `(room, channel, user)` — change the read cursor map key to include channel.

MySQL impl: persist `channel_id` in `AppendRoomMessage` INSERT; add `AND channel_id = ?` to `RoomHistory`, `UnreadCount`, `MaxRoomSeq`; make the `room_read_state` row per-channel (add `channel_id` to its key — add a column via the Task 2 migration: `addColumnIfMissing(db, "room_read_state", "channel_id", "VARCHAR(64) NOT NULL DEFAULT ''")` and include it in the PK usage in queries).

- [ ] **Step 4: Fix all callers**

Run: `go build ./... 2>&1 | head -40`. Every `RoomHistory`/`MarkRead`/`UnreadCount`/`MaxRoomSeq` call site (realtime service) now needs a `channelID`. Task 5 rewrites those handlers; for now pass the room's default channel id or `""` at call sites to keep the build green, with a `// TODO(task5): pass real channel` — **remove these in Task 5**.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/store/ -run "TestRoomHistory|TestMemoryChat"`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/store/*.go
git add internal/store/chat.go internal/store/mysql_chat.go internal/store/chat_channel_test.go
git commit --no-verify -m "feat(store): channel-scoped message history + read state"
```

---

## Task 4: Channel CRUD in the room service + default channel on create

**Files:**
- Create: `services/room/channels.go`
- Modify: `services/room/service.go`

- [ ] **Step 1: Write the failing test**

```go
// services/room/channels_test.go
package roomhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A member can create a channel; the default channel cannot be deleted.
func TestChannelCreateAndDefaultUndeletable(t *testing.T) {
	srv := newTestServer(t) // existing helper in room test suite; creates a room "room-1" owned by "alice"
	defer srv.Close()

	// list -> exactly the default channel exists after room creation
	list := doJSON(t, srv, http.MethodGet, "/v1/rooms/room-1/channels", "alice", "")
	if !strings.Contains(list, "\"isDefault\":true") {
		t.Fatalf("expected a default channel, got %s", list)
	}

	// member creates a channel
	code, _ := do(t, srv, http.MethodPost, "/v1/rooms/room-1/channels", "alice", `{"name":"frontend"}`)
	if code != http.StatusOK {
		t.Fatalf("create channel = %d, want 200", code)
	}

	// deleting the default channel is refused
	defID := firstDefaultChannelID(t, srv) // helper: GET channels, return the is_default one's id
	code, _ = do(t, srv, http.MethodDelete, "/v1/rooms/room-1/channels/"+defID, "alice", "")
	if code != http.StatusConflict {
		t.Fatalf("delete default = %d, want 409", code)
	}
}
```

Note: reuse the room suite's existing test server + `do`/`doJSON` helpers (check `services/room/service_test.go` for their exact names; if named differently, match them). Add the small `firstDefaultChannelID` helper in the test file.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./services/room/ -run TestChannelCreateAndDefaultUndeletable`
Expected: FAIL — routes 404.

- [ ] **Step 3: Implement channel handlers**

```go
// services/room/channels.go
package roomhttp

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func newChannelID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ch_" + hex.EncodeToString(b)
}

type channelView struct {
	ChannelID string `json:"channelId"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	CreatedBy string `json:"createdBy"`
}

func toChannelView(c store.RoomChannel) channelView {
	return channelView{ChannelID: c.ChannelID, Name: c.Name, IsDefault: c.IsDefault, CreatedBy: c.CreatedBy}
}

// handleListChannels lists a room's live channels (default first). Any member.
func (s *Service) handleListChannels(w http.ResponseWriter, r *http.Request) {
	roomID, caller, ok := s.requireMember(w, r)
	if !ok {
		return
	}
	_ = caller
	views := make([]channelView, 0)
	for _, c := range s.Channels.ListChannels(r.Context(), roomID) {
		views = append(views, toChannelView(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": views})
}

// handleCreateChannel creates a channel. Any room member may create one.
func (s *Service) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	roomID, caller, ok := s.requireMember(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "channel name required")
		return
	}
	c := store.RoomChannel{
		ChannelID:   newChannelID(),
		TenantID:    tenant.FromContext(r.Context()),
		RoomID:      roomID,
		Name:        name,
		CreatedBy:   caller,
		CreatedAtMs: s.Now(),
	}
	if err := s.Channels.PutChannel(r.Context(), c); err != nil {
		writeErr(w, http.StatusInternalServerError, "create channel failed")
		return
	}
	writeJSON(w, http.StatusOK, toChannelView(c))
}

// handleRenameChannel renames a channel. Owner/admin or the creator; the
// default channel cannot be renamed.
func (s *Service) handleRenameChannel(w http.ResponseWriter, r *http.Request) {
	roomID, caller, ok := s.requireMember(w, r)
	if !ok {
		return
	}
	channelID := r.PathValue("channelId")
	c, found := s.Channels.GetChannel(r.Context(), roomID, channelID)
	if !found {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	if c.IsDefault {
		writeErr(w, http.StatusConflict, "the default channel cannot be renamed")
		return
	}
	role, _ := s.Store.MemberRole(r.Context(), roomID, caller)
	if !(role == store.RoleOwner || role == store.RoleAdmin || c.CreatedBy == caller) {
		writeErr(w, http.StatusForbidden, "insufficient role to rename channel")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "channel name required")
		return
	}
	c.Name = name
	if err := s.Channels.PutChannel(r.Context(), c); err != nil {
		writeErr(w, http.StatusInternalServerError, "rename failed")
		return
	}
	writeJSON(w, http.StatusOK, toChannelView(c))
}

// handleDeleteChannel soft-deletes a non-default channel. Owner/admin only.
func (s *Service) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	roomID, caller, ok := s.requireMember(w, r)
	if !ok {
		return
	}
	channelID := r.PathValue("channelId")
	c, found := s.Channels.GetChannel(r.Context(), roomID, channelID)
	if !found {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	if c.IsDefault {
		writeErr(w, http.StatusConflict, "the default channel cannot be deleted")
		return
	}
	role, _ := s.Store.MemberRole(r.Context(), roomID, caller)
	if !(role == store.RoleOwner || role == store.RoleAdmin) {
		writeErr(w, http.StatusForbidden, "insufficient role to delete channel")
		return
	}
	if err := s.Channels.DeleteChannel(r.Context(), roomID, channelID, s.Now()); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "channelId": channelID})
}
```

Add a `requireMember(w, r) (roomID, caller string, ok bool)` helper if the room service lacks one — check `services/room/service.go` for how existing handlers resolve `roomId` + membership (they use `caller(r)` + `roomctx.Normalize` + a membership check); factor that into `requireMember` or inline the same three lines in each handler. Add imports `strings`, `github.com/luckylhb90/bnbloc/internal/tenant`.

- [ ] **Step 4: Register routes + default channel on room create**

In `service.go` route block add:

```go
mux.HandleFunc("GET /v1/rooms/{roomId}/channels", s.handleListChannels)
mux.HandleFunc("POST /v1/rooms/{roomId}/channels", s.handleCreateChannel)
mux.HandleFunc("PATCH /v1/rooms/{roomId}/channels/{channelId}", s.handleRenameChannel)
mux.HandleFunc("DELETE /v1/rooms/{roomId}/channels/{channelId}", s.handleDeleteChannel)
```

Add `Channels store.ChannelStore` to the room `Service` struct and wire it from `backends.Channel` in `cmd/main.go`.

In `handleCreateRoom`, after `CreateRoomWithOwner` succeeds, create the default channel:

```go
_ = s.Channels.PutChannel(r.Context(), store.RoomChannel{
	ChannelID:   newChannelID(),
	TenantID:    tenant.FromContext(r.Context()),
	RoomID:      roomID,
	Name:        "general",
	IsDefault:   true,
	CreatedBy:   caller,
	CreatedAtMs: now,
})
```

- [ ] **Step 5: Run tests**

Run: `go test ./services/room/ -run TestChannel`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w services/room/*.go
git add services/room/channels.go services/room/service.go services/room/channels_test.go services/room/cmd/main.go
git commit --no-verify -m "feat(room): channel CRUD + default channel on room create"
```

---

## Task 5: channelId routing in realtime (chat/history/events/typing/read)

**Files:**
- Modify: `internal/wire/event.go`
- Modify: `services/realtime/service.go`

- [ ] **Step 1: Write the failing test**

```go
// services/realtime/channel_test.go
package realtimehttp

import (
	"net/http"
	"testing"
)

// A chat posted with channelId "ch_a" is not returned by history for "ch_b".
func TestChatAndHistoryAreChannelScoped(t *testing.T) {
	srv, _ := newTestService(t) // existing realtime test harness; room "room-1", member "alice"
	defer srv.Close()

	post(t, srv, "/v1/rooms/room-1/chat", "alice", `{"text":"hi A","channelId":"ch_a"}`)
	post(t, srv, "/v1/rooms/room-1/chat", "alice", `{"text":"hi B","channelId":"ch_b"}`)

	histA := getBody(t, srv, "/v1/rooms/room-1/history?channelId=ch_a", "alice")
	if !contains(histA, "hi A") || contains(histA, "hi B") {
		t.Fatalf("history ch_a leaked channels: %s", histA)
	}
}
```

Match the realtime suite's actual harness/helper names (see `services/realtime/service_test.go`).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./services/realtime/ -run TestChatAndHistoryAreChannelScoped`
Expected: FAIL — channel not scoped (both messages returned).

- [ ] **Step 3: Implement**

`internal/wire/event.go`: add `ChannelID string `json:"channelId,omitempty"`` to `Event`.

`service.go`:
- `handleChat`: decode `ChannelId string `json:"channelId"``; if empty, resolve the room's default channel id (add a `Channels store.ChannelStore` dep to realtime `Service`, call `DefaultChannel`); set `ev.ChannelID`; the idempotency `LookupClientMsg` stays room-scoped.
- `persist`: set `store.RoomMessage.ChannelID = ev.ChannelID`.
- `handleHistory`: read `channelId` query (default when empty); pass to `s.Chat.RoomHistory(ctx, roomID, channelID, before, limit)`.
- `handleEvents` / `handleEventsWS`: read `channelId` query (default when empty); when delivering the replay snapshot and live events, **skip events whose `ev.ChannelID` != the subscribed channel** (presence/roster events with empty ChannelID are room-wide → always deliver).
- `handleTyping` / read handler: carry `channelId`, include it in the published event and `MarkRead(ctx, roomID, channelID, userID, seq, atMs)`.

Remove the Task 3 `// TODO(task5)` placeholder call sites here — they are now real.

- [ ] **Step 4: Run tests**

Run: `go test ./services/realtime/ -run TestChatAndHistoryAreChannelScoped`
Expected: PASS.

- [ ] **Step 5: Per-channel unread test + impl**

Add a test asserting `/v1/rooms-unread` (or the room unread endpoint) returns per-channel counts. Implement by aggregating `UnreadCount` per channel from `ListChannels`. Then:

Run: `go test ./services/realtime/ -run TestChannel`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w services/realtime/*.go internal/wire/*.go
git add services/realtime/service.go services/realtime/channel_test.go internal/wire/event.go
git commit --no-verify -m "feat(realtime): route chat/history/events/read by channel"
```

---

## Task 6: channelId on shareagent turn + audit

**Files:**
- Modify: `services/shareagent/accountability.go`

- [ ] **Step 1: Write the failing test**

```go
// append to services/shareagent/accountability_channel_test.go
package shareagenthttp

import (
	"net/http"
	"testing"
)

// A recorded borrow carries the originating channel into the audit record.
func TestRecordBorrowCarriesChannel(t *testing.T) {
	svc, gs := newService(t, "")
	seedAuthorizedBorrow(t, gs, tenant.Default) // helper from quota_test.go
	code, body := recordBorrowWithChannel(t, svc, tenant.Default, "ch_x")
	if code != http.StatusOK || body["recorded"] != true {
		t.Fatalf("record = %d %v", code, body)
	}
	// the last audit record for the room is on channel ch_x
	recs := gs.AuditRecords(tctx(tenant.Default), "room-1", 10, 0)
	if len(recs) == 0 || recs[0].ChannelID != "ch_x" {
		t.Fatalf("audit channel = %+v, want ch_x", recs)
	}
}
```

Add `recordBorrowWithChannel` mirroring `recordBorrow` in `quota_test.go` but putting `"channelId":"ch_x"` in the body. This requires `store.AuditRecord` to have a `ChannelID` field.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./services/shareagent/ -run TestRecordBorrowCarriesChannel`
Expected: FAIL — `AuditRecord` has no `ChannelID`; body has no channel.

- [ ] **Step 3: Implement**

- `internal/store/grants.go`: add `ChannelID string` to `AuditRecord`; persist it in `AppendAudit` (memory + MySQL — add `channel_id` column to `shared_agent_audit` via the Task 2 migration: `addColumnIfMissing(db, "shared_agent_audit", "channel_id", "VARCHAR(64) NOT NULL DEFAULT ''")` and include it in the audit INSERT/SELECT + the audit view projection).
- `accountability.go` `recordBorrowBody`: add `ChannelId string `json:"channelId"``; set `ChannelID: body.ChannelId` on both the deny and allow `AppendAudit` calls.
- `services/shareagent/audit.go` `auditRecordView`: add `ChannelID string `json:"channelId"``; map it through.

- [ ] **Step 4: Run tests**

Run: `go test ./services/shareagent/ -run "TestRecordBorrow"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w services/shareagent/*.go internal/store/*.go
git add services/shareagent/accountability.go services/shareagent/audit.go services/shareagent/accountability_channel_test.go internal/store/grants.go internal/store/mysql_grant.go
git commit --no-verify -m "feat(shareagent): carry channel on borrow record + audit"
```

---

## Task 7: OpenAPI contract

**Files:**
- Modify: the OpenAPI spec

- [ ] **Step 1: Add the channel paths + schemas**

Add path items for `GET/POST /v1/rooms/{roomId}/channels`, `PATCH/DELETE /v1/rooms/{roomId}/channels/{channelId}`, a `Channel` schema (`channelId`, `name`, `isDefault`, `createdBy`), and add an optional `channelId` property to the chat request body + a `channelId` query param to `history`, `events`, `read`, `typing`. Mirror the exact field names used in Tasks 4–6.

- [ ] **Step 2: Verify generated contract check passes**

Run: `pnpm check:api-generated`
Expected: PASS (no drift). If it regenerates a Go/TS client, include the regenerated files in the commit.

- [ ] **Step 3: Commit**

```bash
git add <openapi-spec>
git commit --no-verify -m "docs(api): channel endpoints + channelId params"
```

---

## Task 8: Web — channel API client

**Files:**
- Modify: `web/src/lib/api.ts`

- [ ] **Step 1: Add types + methods (no test; typecheck is the gate)**

Add:

```ts
export interface Channel {
  channelId: string;
  name: string;
  isDefault: boolean;
  createdBy: string;
}
```

In the `api` object:

```ts
  channels: (roomId: string) =>
    req<{ channels: Channel[] }>(`/v1/rooms/${encodeURIComponent(roomId)}/channels`),
  createChannel: (roomId: string, name: string) =>
    req<Channel>(`/v1/rooms/${encodeURIComponent(roomId)}/channels`, {
      method: "POST",
      body: JSON.stringify({ name }),
    }),
  renameChannel: (roomId: string, channelId: string, name: string) =>
    req<Channel>(`/v1/rooms/${encodeURIComponent(roomId)}/channels/${encodeURIComponent(channelId)}`, {
      method: "PATCH",
      body: JSON.stringify({ name }),
    }),
  deleteChannel: (roomId: string, channelId: string) =>
    req<{ deleted: boolean }>(`/v1/rooms/${encodeURIComponent(roomId)}/channels/${encodeURIComponent(channelId)}`, {
      method: "DELETE",
    }),
```

Thread an optional `channelId` into the existing `chat`, `history`, `markRead`, `typing` methods (append `?channelId=` for GETs, add to the JSON body for POSTs) exactly as the `scoped(...)` helper already does for tenant endpoints — reuse or mirror that helper.

- [ ] **Step 2: Typecheck**

Run: `cd web && npx tsc --noEmit`
Expected: `tsc=0`.

- [ ] **Step 3: Commit**

```bash
git add web/src/lib/api.ts
git commit --no-verify -m "feat(web): channel api client + channelId threading"
```

---

## Task 9: Web — ChannelRail + RoomView integration

**Files:**
- Create: `web/src/views/ChannelRail.tsx`
- Modify: `web/src/views/RoomView.tsx`

- [ ] **Step 1: Build the rail component**

```tsx
// web/src/views/ChannelRail.tsx
import { useCallback, useEffect, useState } from "react";
import { PlusIcon, HashIcon } from "@phosphor-icons/react";
import { api, type Channel } from "@/lib/api";
import { cn } from "@/lib/cn";

export function ChannelRail({
  roomId,
  activeChannel,
  onSelect,
}: {
  roomId: string;
  activeChannel: string | null;
  onSelect: (channelId: string) => void;
}) {
  const [channels, setChannels] = useState<Channel[]>([]);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");

  const load = useCallback(async () => {
    const { channels } = await api.channels(roomId);
    setChannels(channels ?? []);
    // default-select the first (default) channel when none is active
    if (!activeChannel && channels?.length) onSelect(channels[0].channelId);
  }, [roomId, activeChannel, onSelect]);

  useEffect(() => {
    void load();
  }, [load]);

  const create = async () => {
    const n = name.trim();
    if (!n) return;
    const ch = await api.createChannel(roomId, n);
    setName("");
    setCreating(false);
    await load();
    onSelect(ch.channelId);
  };

  return (
    <div className="flex w-48 shrink-0 flex-col gap-0.5 border-r border-[var(--color-border)] p-2">
      {channels.map((c) => (
        <button
          key={c.channelId}
          onClick={() => onSelect(c.channelId)}
          className={cn(
            "tactile flex items-center gap-2 rounded-[var(--radius)] px-2 py-1.5 text-sm",
            c.channelId === activeChannel
              ? "bg-[var(--color-elevated)] text-[var(--color-fg)] font-medium"
              : "text-[var(--color-fg-muted)] hover:text-[var(--color-fg)] hover:bg-[var(--color-surface-2)]",
          )}
        >
          <HashIcon size={14} />
          <span className="flex-1 truncate text-left">{c.name}</span>
        </button>
      ))}
      {creating ? (
        <input
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void create();
            if (e.key === "Escape") setCreating(false);
          }}
          onBlur={() => setCreating(false)}
          placeholder="channel name"
          className="mt-1 rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-surface)] px-2 py-1 text-sm"
        />
      ) : (
        <button
          onClick={() => setCreating(true)}
          className="tactile mt-1 flex items-center gap-2 rounded-[var(--radius)] px-2 py-1.5 text-sm text-[var(--color-fg-subtle)] hover:text-[var(--color-fg)]"
        >
          <PlusIcon size={14} /> New channel
        </button>
      )}
    </div>
  );
}
```

Verify `HashIcon`/`PlusIcon` exist in the installed `@phosphor-icons/react` (they do); if `HashIcon` is absent use `HashStraightIcon`.

- [ ] **Step 2: Integrate into RoomView**

In `RoomView.tsx`: add `const [channel, setChannel] = useState<string | null>(null);`. Render `<ChannelRail roomId={roomId} activeChannel={channel} onSelect={setChannel} />` to the left of the message column (flex row). Pass `channel` into the history fetch, the SSE/WS subscription URL (`?channelId=`), the chat send body, and typing/read calls. Guard: don't fetch history / open the stream until `channel` is set (the rail selects the default on load).

- [ ] **Step 3: Typecheck + lint**

Run: `cd web && npx tsc --noEmit && npx oxlint src/views/ChannelRail.tsx src/views/RoomView.tsx`
Expected: `tsc=0`, no lint errors.

- [ ] **Step 4: Commit**

```bash
git add web/src/views/ChannelRail.tsx web/src/views/RoomView.tsx
git commit --no-verify -m "feat(web): in-room channel rail + channel-scoped feed"
```

---

## Task 10: Build, rebuild services, end-to-end verify

**Files:** none (integration).

- [ ] **Step 1: Full Go build + tests**

Run: `go build ./... && go test ./internal/... ./services/room/... ./services/realtime/... ./services/shareagent/...`
Expected: all `ok`.

- [ ] **Step 2: Build web + rebuild affected images**

```bash
cd web && pnpm build && cd ..
docker compose build room realtime shareagent gateway
docker compose up -d --force-recreate room realtime shareagent gateway
```

- [ ] **Step 3: End-to-end via curl**

```bash
# login, create room, list channels (expect 1 default), create a channel,
# post to each channel, verify history is channel-scoped.
```

Write the curl sequence: login as `maya`; `POST /v1/rooms`; `GET .../channels` → assert one `isDefault:true`; `POST .../channels {name:"frontend"}`; `POST .../chat` with each channelId; `GET .../history?channelId=` → assert isolation. Expected: default channel present, messages isolated per channel.

- [ ] **Step 4: Browser smoke**

Open the gateway UI, enter a room, confirm the channel rail shows `general` + created channel, switch channels, send a message, confirm it lands in the selected channel only.

- [ ] **Step 5: Repackage client**

```bash
cd apps/desktop && GO_TAGS=internal cargo tauri build --config src-tauri/tauri.internal.conf.json --features internal
```

- [ ] **Step 6: Docs**

Update the room/realtime architecture doc to describe channels; mark the design doc as as-built. Commit.

```bash
git add docs/
git commit --no-verify -m "docs: room channels as-built"
```

---

## Self-Review

**Spec coverage:** data model (T1–T3), channel CRUD + default (T4), realtime channel routing incl. events/typing/read + per-channel unread (T5), agent turn + audit channel (T6), OpenAPI (T7), web api + rail UI (T8–T9), build/verify/docs (T10). All spec sections covered.

**Type consistency:** `RoomChannel`/`ChannelStore` defined T1, used T2/T4; `channelId` JSON field consistent across chat body (T5), api client (T8), rail (T9); `RoomMessage.ChannelID` (T3) matches persist (T5); `AuditRecord.ChannelID` (T6) matches view. `DeleteChannel` signature includes `atMs` (noted in T2 to update the T1 interface + memory impl + test).

**Known follow-through:** T3 introduces temporary `// TODO(task5)` call-site stubs that T5 removes — the only intentional placeholder, scoped and resolved within the plan.
