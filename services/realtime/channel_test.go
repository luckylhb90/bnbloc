package realtimehttp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// dctx scopes direct channel-store seeds to the default tenant, matching what
// svcauth.EnsureTenant (wrapped by Handler) injects for HTTP requests, so the
// seeds land in the tenant the handlers read.
func dctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

// newChannelTestServer builds the realtime service with durable chat and
// channel stores: room "room-1" with members "alice" (author) and "bob"
// (reader), and channels ch_general (default), ch_a, and ch_b. CreatedAtMs
// values are distinct so channel ordering (default first, then age) — and thus
// the per-channel unread order — is deterministic.
func newChannelTestServer(t *testing.T) (*httptest.Server, *Service) {
	t.Helper()
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	mem.Add("room-1", "bob")
	channels := store.NewMemoryChannel()
	seed := func(id string, createdMs int64, def bool) {
		t.Helper()
		if err := channels.PutChannel(dctx(), store.RoomChannel{
			ChannelID: id, TenantID: tenant.Default, RoomID: "room-1",
			IsDefault: def, CreatedBy: "alice", CreatedAtMs: createdMs,
		}); err != nil {
			t.Fatalf("seed channel %s: %v", id, err)
		}
	}
	seed("ch_general", 1, true)
	seed("ch_a", 2, false)
	seed("ch_b", 3, false)
	svc := &Service{
		Hub:        realtime.NewHub(64, 16),
		Membership: mem,
		Chat:       store.NewMemoryChat(),
		Channels:   channels,
	}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return srv, svc
}

// channelPost posts a JSON body as user and returns the response.
func channelPost(t *testing.T, srv *httptest.Server, path, user, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("X-Bnbloc-User", user)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// channelGet fetches as user, asserting 200, and returns the response body.
func channelGet(t *testing.T, srv *httptest.Server, path, user string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", path, resp.StatusCode)
	}
	buf, _ := io.ReadAll(resp.Body)
	return string(buf)
}

// postChat sends a chat message (optionally addressed to a channel) and returns
// its assigned sequence.
func postChat(t *testing.T, srv *httptest.Server, user, text, channelID string) int64 {
	t.Helper()
	body := map[string]any{"text": text}
	if channelID != "" {
		body["channelId"] = channelID
	}
	buf, _ := json.Marshal(body)
	resp := channelPost(t, srv, "/v1/rooms/room-1/chat", user, string(buf))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Sequence int64 `json:"sequence"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Sequence
}

// historyTexts returns the text of every message in one channel-scoped history
// page, in page order.
func historyTexts(t *testing.T, srv *httptest.Server, user, query string) []string {
	t.Helper()
	body := channelGet(t, srv, "/v1/rooms/room-1/history"+query, user)
	var res struct {
		Messages []struct {
			Payload struct {
				Text string `json:"text"`
			} `json:"payload"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("history decode %q: %v", body, err)
	}
	texts := make([]string, 0, len(res.Messages))
	for _, m := range res.Messages {
		texts = append(texts, m.Payload.Text)
	}
	return texts
}

// unreadChannelCount is one channel's entry in the rooms-unread response.
type unreadChannelCount struct {
	ChannelID string `json:"channelId"`
	Count     int    `json:"count"`
}

// unreadSnapshot is the decoded rooms-unread response.
type unreadSnapshot struct {
	Unread   map[string]int                  `json:"unread"`
	Channels map[string][]unreadChannelCount `json:"channels"`
}

// fetchUnread reads the caller's per-room and per-channel unread counts.
func fetchUnread(t *testing.T, srv *httptest.Server, user string) unreadSnapshot {
	t.Helper()
	var res unreadSnapshot
	if err := json.Unmarshal([]byte(channelGet(t, srv, "/v1/rooms-unread?rooms=room-1", user)), &res); err != nil {
		t.Fatalf("unread decode: %v", err)
	}
	return res
}

// sseEvents parses an SSE body into a channel of decoded events, following the
// service_test.go reader idiom.
func sseEvents(t *testing.T, body io.Reader) <-chan wire.Event {
	t.Helper()
	out := make(chan wire.Event, 32)
	go func() {
		defer close(out)
		reader := bufio.NewReader(body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev wire.Event
			if err := json.Unmarshal([]byte(strings.TrimRight(line[len("data: "):], "\n")), &ev); err == nil {
				out <- ev
			}
		}
	}()
	return out
}

// A chat posted to ch_a is not returned by ch_b's history: history pages are
// scoped to exactly one channel.
func TestChatAndHistoryAreChannelScoped(t *testing.T) {
	srv, _ := newChannelTestServer(t)
	postChat(t, srv, "alice", "hi A", "ch_a")
	postChat(t, srv, "alice", "hi B", "ch_b")

	if got := historyTexts(t, srv, "alice", "?channelId=ch_a"); len(got) != 1 || got[0] != "hi A" {
		t.Fatalf("history ch_a = %v, want only [hi A]", got)
	}
	if got := historyTexts(t, srv, "alice", "?channelId=ch_b"); len(got) != 1 || got[0] != "hi B" {
		t.Fatalf("history ch_b = %v, want only [hi B]", got)
	}
}

// A chat posted without channelId lands in the room's default channel: history
// for the default channel returns it, channel-less history reads the same
// default, and another channel's history stays empty. Reading channel-less
// clears the default channel's unread (same default resolution on the read
// path).
func TestChatWithoutChannelLandsInDefault(t *testing.T) {
	srv, _ := newChannelTestServer(t)
	seq := postChat(t, srv, "alice", "hello default", "")

	if got := historyTexts(t, srv, "alice", "?channelId=ch_general"); len(got) != 1 || got[0] != "hello default" {
		t.Fatalf("default-channel history = %v, want [hello default]", got)
	}
	if got := historyTexts(t, srv, "alice", ""); len(got) != 1 || got[0] != "hello default" {
		t.Fatalf("channel-less history = %v, want the default channel's [hello default]", got)
	}
	if got := historyTexts(t, srv, "alice", "?channelId=ch_a"); len(got) != 0 {
		t.Fatalf("ch_a history = %v, want empty", got)
	}

	if res := fetchUnread(t, srv, "alice"); res.Unread["room-1"] != 1 {
		t.Fatalf("unread after posting = %d, want 1", res.Unread["room-1"])
	}
	read := channelPost(t, srv, "/v1/rooms/room-1/read", "alice", fmt.Sprintf(`{"seq":%d}`, seq))
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d, want 200", read.StatusCode)
	}
	if res := fetchUnread(t, srv, "alice"); res.Unread["room-1"] != 0 {
		t.Fatalf("unread after channel-less read = %d, want 0 (the read resolved the default channel)", res.Unread["room-1"])
	}
}

// An SSE subscriber following ch_a receives ch_a's chat and typing events but
// never ch_b's: both the replay snapshot and live deliveries are filtered
// server-side, while room-wide events (presence) still arrive. Frames arrive in
// publish order, so receiving the later live ch_a chat and the typing hint
// proves every earlier ch_b event (replay and live) was filtered out.
func TestSSEStreamIsChannelScoped(t *testing.T) {
	srv, svc := newChannelTestServer(t)

	// Two messages land in the replay buffer before anyone subscribes.
	replayA := postChat(t, srv, "alice", "replay A", "ch_a")
	postChat(t, srv, "alice", "replay B", "ch_b")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/events?channelId=ch_a", nil)
	req.Header.Set("X-Bnbloc-User", "bob")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe status = %d, want 200", resp.StatusCode)
	}
	if !waitFor(func() bool { return svc.Hub.SubscriberCount("room-1") == 1 }) {
		t.Fatal("timed out waiting for the subscriber to register")
	}

	// Live traffic after subscribing: ch_a chat, ch_b chat (must be skipped),
	// then a ch_a typing hint.
	liveA := postChat(t, srv, "alice", "live A", "ch_a")
	postChat(t, srv, "alice", "live B", "ch_b")
	typing := channelPost(t, srv, "/v1/rooms/room-1/typing", "alice", `{"channelId":"ch_a"}`)
	if typing.StatusCode != http.StatusOK {
		t.Fatalf("typing status = %d, want 200", typing.StatusCode)
	}

	events := sseEvents(t, resp.Body)
	var sawReplayA, sawLiveA, sawTyping bool
	deadline := time.After(4 * time.Second)
	for !(sawReplayA && sawLiveA && sawTyping) {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before ch_a chat and typing arrived")
			}
			// Room-wide events (presence, roster) carry no channel; everything
			// else must be the subscribed channel.
			if ev.ChannelID != "" && ev.ChannelID != "ch_a" {
				t.Fatalf("received event for channel %q (kind=%s)", ev.ChannelID, ev.Kind)
			}
			switch {
			case ev.Kind == "chat_message" && ev.Sequence == replayA:
				sawReplayA = true
			case ev.Kind == "chat_message" && ev.Sequence == liveA:
				sawLiveA = true
			case ev.Kind == "presence_typing":
				if ev.ChannelID != "ch_a" {
					t.Fatalf("typing event channel = %q, want ch_a", ev.ChannelID)
				}
				sawTyping = true
			}
		case <-deadline:
			t.Fatalf("timed out: replayA=%v liveA=%v typing=%v", sawReplayA, sawLiveA, sawTyping)
		}
	}
}

// Unread counts break down per channel, and a read on one channel clears only
// that channel's count: the room total is the sum across channels, the
// per-channel list drops to the unread ones, and the default channel (with no
// messages) never appears.
func TestUnreadPerChannelAndMarkReadScoped(t *testing.T) {
	srv, _ := newChannelTestServer(t)
	postChat(t, srv, "alice", "a1", "ch_a")
	seqA2 := postChat(t, srv, "alice", "a2", "ch_a")
	postChat(t, srv, "alice", "b1", "ch_b")

	res := fetchUnread(t, srv, "bob")
	if res.Unread["room-1"] != 3 {
		t.Fatalf("room unread = %d, want 3 (2 in ch_a + 1 in ch_b)", res.Unread["room-1"])
	}
	if got := res.Channels["room-1"]; len(got) != 2 ||
		got[0].ChannelID != "ch_a" || got[0].Count != 2 ||
		got[1].ChannelID != "ch_b" || got[1].Count != 1 {
		t.Fatalf("per-channel unread = %+v, want ch_a:2 then ch_b:1", got)
	}

	// Bob reads ch_a up to its latest sequence; ch_b's badge survives.
	read := channelPost(t, srv, "/v1/rooms/room-1/read", "bob", fmt.Sprintf(`{"seq":%d,"channelId":"ch_a"}`, seqA2))
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d, want 200", read.StatusCode)
	}
	res = fetchUnread(t, srv, "bob")
	if res.Unread["room-1"] != 1 {
		t.Fatalf("room unread after reading ch_a = %d, want 1", res.Unread["room-1"])
	}
	if got := res.Channels["room-1"]; len(got) != 1 || got[0].ChannelID != "ch_b" || got[0].Count != 1 {
		t.Fatalf("per-channel unread after read = %+v, want only ch_b:1", got)
	}
}
