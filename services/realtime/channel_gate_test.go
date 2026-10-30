package realtimehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The channel gate (channel_gate.go) and the agents' admission when a
// channel's settings change (docs/prd/2026-10-28-channels-and-threads.md,
// slice 1), over the memory room store the room service writes.

type gateEnv struct {
	t     *testing.T
	srv   *httptest.Server
	svc   *Service
	rooms *store.MemoryRoom
	now   time.Time
	mu    sync.Mutex
}

// newGateEnv: room-1 owned by alice, with admin erin, members bob and carol
// and an agent host (hoster); channels ch_general (default), ch_a, ch_b.
func newGateEnv(t *testing.T) *gateEnv {
	t.Helper()
	rooms := store.NewMemoryRoom()
	ctx := dctx()
	if err := rooms.CreateRoomWithOwnerAndChannel(ctx, store.Room{RoomID: "room-1", Name: "Team", CreatedAtMs: 1},
		store.Membership{RoomID: "room-1", UserID: "alice", Role: store.RoleOwner, JoinedAtMs: 1},
		store.RoomChannel{ChannelID: "ch_general", RoomID: "room-1", Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	mem := membership.NewStatic()
	mem.AddRole("room-1", "alice", store.RoleOwner)
	for user, role := range map[string]store.Role{"erin": store.RoleAdmin, "bob": store.RoleMember, "carol": store.RoleMember} {
		if err := rooms.AddMember(ctx, store.Membership{RoomID: "room-1", UserID: user, Role: role, JoinedAtMs: 2}); err != nil {
			t.Fatal(err)
		}
		mem.AddRole("room-1", user, role)
	}
	if err := mem.PutAgentHost(ctx, tenant.Default, "room-1", "hoster"); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"ch_a", "ch_b"} {
		if err := rooms.Channels().PutChannel(ctx, store.RoomChannel{ChannelID: id, RoomID: "room-1", Name: id, CreatedBy: "alice", OwnerID: "alice", CreatedAtMs: int64(2 + i)}); err != nil {
			t.Fatal(err)
		}
	}
	e := &gateEnv{t: t, rooms: rooms, now: time.UnixMilli(1_000_000)}
	e.svc = &Service{
		Hub: realtime.NewHub(64, 16), Membership: mem, Chat: store.NewMemoryChat(),
		Channels: rooms.Channels(), RoomRoles: rooms,
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
	}
	e.srv = httptest.NewServer(e.svc.Handler())
	t.Cleanup(e.srv.Close)
	return e
}

func (e *gateEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

// setSettings changes a channel's settings as the room service does: the
// store, then channel_updated (room-wide).
func (e *gateEnv) setSettings(channelID string, s wire.ChannelSettings) {
	e.t.Helper()
	if err := e.rooms.Channels().UpdateChannel(dctx(), "room-1", channelID, store.ChannelPatch{Settings: &s}); err != nil {
		e.t.Fatal(err)
	}
	e.channelUpdated(channelID)
}

// roomUpdated announces a change of the room's settings or of userID's
// agreement, as the room service does (wire.EventRoomUpdated).
func (e *gateEnv) roomUpdated(userID string) {
	e.t.Helper()
	if _, err := e.svc.publish(dctx(), wire.Event{RoomID: "room-1", Kind: wire.EventRoomUpdated,
		Payload: wire.RoomUpdatedPayload{UserID: userID}, AtUnixMs: 1}, realtime.Durable{}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *gateEnv) channelUpdated(channelID string) {
	e.t.Helper()
	if _, err := e.svc.publish(dctx(), wire.Event{RoomID: "room-1", Kind: wire.EventChannelUpdated,
		Payload: wire.ChannelUpdatedPayload{ChannelID: channelID}, AtUnixMs: 1}, realtime.Durable{}); err != nil {
		e.t.Fatal(err)
	}
}

// post sends a chat as user and returns the status and the error body.
func (e *gateEnv) post(user, channelID, text string, recipient string) (int, httpx.ErrorBody, http.Header) {
	e.t.Helper()
	body := fmt.Sprintf(`{"text":%q,"channelId":%q%s}`, text, channelID, recipient)
	resp := channelPost(e.t, e.srv, "/v1/rooms/room-1/chat", user, body)
	raw, _ := io.ReadAll(resp.Body)
	var eb httpx.ErrorBody
	_ = json.Unmarshal(raw, &eb)
	return resp.StatusCode, eb, resp.Header
}

func (e *gateEnv) mustPost(user, channelID, text string) {
	e.t.Helper()
	if code, eb, _ := e.post(user, channelID, text, ""); code != http.StatusOK {
		e.t.Fatalf("post by %s in %s = %d %+v", user, channelID, code, eb)
	}
}

func (e *gateEnv) refused(user, channelID string, wantStatus int, wantCode string) httpx.ErrorBody {
	e.t.Helper()
	code, eb, _ := e.post(user, channelID, "hello", "")
	if code != wantStatus || eb.Code != wantCode {
		e.t.Fatalf("post by %s in %s = %d %+v, want %d %s", user, channelID, code, eb, wantStatus, wantCode)
	}
	return eb
}

// An archived channel is read-only (C10): every post is refused with its
// code, its history stays readable, and a restore opens it again.
func TestGateArchivedChannelIsReadOnly(t *testing.T) {
	e := newGateEnv(t)
	e.mustPost("bob", "ch_a", "before")
	at := int64(5)
	if err := e.rooms.Channels().SetChannelArchived(dctx(), "room-1", "ch_a", &at); err != nil {
		t.Fatal(err)
	}
	e.channelUpdated("ch_a")
	e.refused("bob", "ch_a", http.StatusForbidden, wire.ChannelArchived)
	e.refused("alice", "ch_a", http.StatusForbidden, wire.ChannelArchived)
	if got := historyTexts(t, e.srv, "carol", "?channelId=ch_a"); len(got) != 1 || got[0] != "before" {
		t.Fatalf("an archived channel's history = %v", got)
	}
	if err := e.rooms.Channels().SetChannelArchived(dctx(), "room-1", "ch_a", nil); err != nil {
		t.Fatal(err)
	}
	e.channelUpdated("ch_a")
	e.mustPost("bob", "ch_a", "after")
}

// "Admins" posting: only the room's owner and admins post; members are
// refused with the code, in that channel only.
func TestGateAdminsOnlyPosting(t *testing.T) {
	e := newGateEnv(t)
	e.setSettings("ch_a", wire.ChannelSettings{WhoPosts: wire.ChannelPostersAdmins})
	e.refused("bob", "ch_a", http.StatusForbidden, wire.ChannelAdminsOnly)
	e.mustPost("erin", "ch_a", "from an admin")
	e.mustPost("alice", "ch_a", "from the owner")
	e.mustPost("bob", "ch_b", "elsewhere")
}

// Slow mode: one post per member per interval in the channel, refused with
// the wait (body and Retry-After); other members and channels are not held;
// the interval passed, the member posts again.
func TestGateSlowMode(t *testing.T) {
	e := newGateEnv(t)
	e.setSettings("ch_a", wire.ChannelSettings{SlowModeSeconds: 60})
	e.mustPost("bob", "ch_a", "one")
	e.advance(20 * time.Second)
	code, eb, h := e.post("bob", "ch_a", "two", "")
	if code != http.StatusTooManyRequests || eb.Code != wire.ChannelSlowMode || eb.RetryAfterMs != 40_000 || h.Get("Retry-After") != "40" {
		t.Fatalf("second post = %d %+v (Retry-After %q), want 429 slow mode, 40s", code, eb, h.Get("Retry-After"))
	}
	e.mustPost("carol", "ch_a", "another member")
	e.mustPost("bob", "ch_b", "another channel")
	e.advance(40 * time.Second)
	e.mustPost("bob", "ch_a", "three")
	// Turning slow mode off lets the next post through at once.
	e.setSettings("ch_a", wire.ChannelSettings{})
	e.mustPost("bob", "ch_a", "four")
}

// Posting joins once per owner's life, and again after the member left
// (the room service's channel_follow): a later post rejoins.
func TestPostingRejoinsAfterLeaving(t *testing.T) {
	e := newGateEnv(t)
	follows := func() bool {
		t.Helper()
		e.svc.postMarks.flush()
		list, err := e.rooms.Channels().ChannelFollows(dctx(), "room-1", "bob")
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range list {
			if f.ChannelID == "ch_a" {
				return true
			}
		}
		return false
	}
	e.mustPost("bob", "ch_a", "one")
	if !follows() {
		t.Fatal("posting did not join")
	}
	if err := e.rooms.Channels().UnfollowChannel(dctx(), "room-1", "ch_a", "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.publish(dctx(), wire.Event{RoomID: "room-1", ChannelID: "ch_a", Kind: wire.EventChannelFollow,
		Payload: wire.ChannelFollowPayload{ChannelID: "ch_a", UserID: "bob"}, AtUnixMs: 1}, realtime.Durable{}); err != nil {
		t.Fatal(err)
	}
	e.mustPost("bob", "ch_a", "two")
	if !follows() {
		t.Fatal("posting after leaving did not rejoin")
	}
}

// Slow mode holds across a restart of the owner: the member's last post,
// written behind as their follow mark, is read when memory has none.
func TestGateSlowModeAcrossRestart(t *testing.T) {
	e := newGateEnv(t)
	e.setSettings("ch_a", wire.ChannelSettings{SlowModeSeconds: 60})
	e.mustPost("bob", "ch_a", "one")
	e.svc.postMarks.flush()
	e.svc.postMarks = postMarks{} // the owner restarted: nothing in memory
	e.advance(20 * time.Second)
	if code, eb, _ := e.post("bob", "ch_a", "two", ""); code != http.StatusTooManyRequests || eb.RetryAfterMs != 40_000 {
		t.Fatalf("a post 20 s after one before the restart = %d %+v, want 429 with 40 s left", code, eb)
	}
	e.advance(40 * time.Second)
	e.mustPost("bob", "ch_a", "three")
}

// Slow mode over the bus's claims holds whichever replica the member's next
// post reaches: one that never saw the first post (no memory, no mark
// written yet) still refuses it.
func TestGateSlowModeSharedByReplicas(t *testing.T) {
	e := newGateEnv(t)
	e.svc.Claims = &realtime.MemoryClaims{Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }}
	e.setSettings("ch_a", wire.ChannelSettings{SlowModeSeconds: 60})
	e.mustPost("bob", "ch_a", "one")
	e.svc.postMarks = postMarks{} // another replica: nothing in memory, the mark not written
	e.advance(20 * time.Second)
	if code, eb, _ := e.post("bob", "ch_a", "two", ""); code != http.StatusTooManyRequests || eb.RetryAfterMs != 40_000 {
		t.Fatalf("a post 20 s after one on another replica = %d %+v, want 429 with 40 s left", code, eb)
	}
	e.mustPost("carol", "ch_a", "another member")
	e.advance(40 * time.Second)
	e.mustPost("bob", "ch_a", "three")
}

// Agreeing to the rules (C16): a room that requires it refuses a member's
// post until they agree to the current version, and asks again when the
// rules change.
func TestGateRulesAgreement(t *testing.T) {
	e := newGateEnv(t)
	ctx := dctx()
	e.mustPost("bob", "ch_a", "before any rules")
	if err := e.rooms.UpdateRoom(ctx, "room-1", store.RoomPatch{Rules: ptr("Be kind."), RequireRulesAgreement: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	e.roomUpdated("")
	e.refused("bob", "ch_a", http.StatusForbidden, wire.RulesAgreementRequired)
	if err := e.rooms.AgreeRoomRules(ctx, "room-1", "bob", 1); err != nil {
		t.Fatal(err)
	}
	e.roomUpdated("bob")
	e.mustPost("bob", "ch_a", "agreed")
	if err := e.rooms.UpdateRoom(ctx, "room-1", store.RoomPatch{Rules: ptr("Be kinder.")}); err != nil {
		t.Fatal(err)
	}
	e.roomUpdated("")
	e.refused("bob", "ch_a", http.StatusForbidden, wire.RulesAgreementRequired)
	// Off again: nobody is asked.
	if err := e.rooms.UpdateRoom(ctx, "room-1", store.RoomPatch{RequireRulesAgreement: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	e.roomUpdated("")
	e.mustPost("bob", "ch_a", "not required")
}

// Posting joins the channel, and so does being mentioned (C18); a mention
// of someone who is not a member joins nobody.
func TestPostingAndMentionJoin(t *testing.T) {
	e := newGateEnv(t)
	e.mustPost("bob", "ch_a", "hi @carol and @mallory")
	// The poster's mark is written behind (post_marks.go): write it now.
	e.svc.postMarks.flush()
	joined := func(user string) bool {
		list, err := e.rooms.Channels().ChannelFollows(dctx(), "room-1", user)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range list {
			if f.ChannelID == "ch_a" {
				return true
			}
		}
		return false
	}
	if !joined("bob") || !joined("carol") || joined("mallory") || joined("erin") {
		t.Fatalf("joined: bob %v carol %v mallory %v erin %v", joined("bob"), joined("carol"), joined("mallory"), joined("erin"))
	}
}

// A channel's NoAgents toggle reaches agent streams that are already open:
// the bridge's all-channels stream stops receiving the channel once
// channel_updated arrives (its memoized verdict is forgotten) and receives
// it again when the channel admits agents again; an agent recipient there
// is refused with its code; an agent host's one-channel stream ends.
func TestNoAgentsToggleReachesRunningAgentStreams(t *testing.T) {
	e := newGateEnv(t)
	bridge := openPeopleTestStream(t, e.srv, e.svc, "bob", bridgeStreamQuery, 1)
	hosted := openPeopleTestStream(t, e.srv, e.svc, "hoster", "channelId=ch_a&replay=0", 2)

	isText := func(text string) func(wire.Event) bool {
		return func(ev wire.Event) bool {
			p, ok := wire.DecodePayload[wire.ChatMessagePayload](ev.Payload)
			return ev.Kind == wire.KindChatMessage && ok && p.Text == text
		}
	}
	// Admitted: the bridge sees ch_a (and memoizes that verdict).
	e.mustPost("carol", "ch_a", "admitted")
	collectUntil(t, bridge, isText("admitted"))

	e.setSettings("ch_a", wire.ChannelSettings{NoAgents: true})
	e.mustPost("carol", "ch_a", "people only")
	e.mustPost("carol", "ch_b", "marker")
	for _, ev := range collectUntil(t, bridge, isText("marker")) {
		if ev.ChannelID == "ch_a" && ev.Kind == wire.KindChatMessage {
			t.Fatalf("the bridge received a no-agents channel's message: %+v", ev)
		}
	}
	// The agent host's stream on ch_a ended when the channel barred agents.
	deadline := time.After(4 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-hosted:
		case <-deadline:
			t.Fatal("an agent host's stream on a channel that bars agents stayed open")
		}
	}
	code, eb, _ := e.post("carol", "ch_a", "@bob/codex hi", `,"recipient":{"ownerId":"bob","provider":"codex"}`)
	if code != http.StatusBadRequest || eb.Code != wire.AgentsNotAdmitted {
		t.Fatalf("agent recipient in a no-agents channel = %d %+v", code, eb)
	}

	e.setSettings("ch_a", wire.ChannelSettings{})
	e.mustPost("carol", "ch_a", "admitted again")
	collectUntil(t, bridge, isText("admitted again"))
}

// A failed admission read withholds the event from an agent stream (fail
// closed) instead of delivering it.
func TestAgentFilterFailsClosed(t *testing.T) {
	f := &agentFilter{svc: &Service{Channels: failingAdmission{}}, ctx: context.Background(), roomID: "room-1", verdict: map[string]bool{}}
	if !f.skip(wire.Event{ChannelID: "ch_a", Kind: wire.KindChatMessage}) {
		t.Fatal("a failed admission read delivered the event")
	}
	if f.skip(wire.Event{Kind: wire.EventPresenceDelta}) {
		t.Fatal("a room-wide event was withheld")
	}
}

// failingAdmission is a channel store whose admission read fails.
type failingAdmission struct{ ChannelStore }

func (failingAdmission) ChannelAdmission(context.Context, string, string) (store.ChannelAdmission, bool, error) {
	return store.ChannelAdmission{}, false, fmt.Errorf("db down")
}

func ptr[T any](v T) *T { return &v }
