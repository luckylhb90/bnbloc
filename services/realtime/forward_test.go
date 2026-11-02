package realtimehttp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func getStatus(t *testing.T, url, user string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// A forward names its source and carries none of its text; only a reader of
// the source may forward it, and only a reader of the source opens it: a
// member outside a members-only section reads neither the source nor its
// text through the forward.
func TestForwardKeepsTheSourcesAccess(t *testing.T) {
	srv, svc, _ := newMembersOnlyServer(t)
	svc.Membership.(*membership.Static).Add("room-1", "dana")
	open := postChat(t, srv, "alice", "ship on friday", "ch_a")
	secret := postChat(t, srv, "alice", "the secret plan", "ch_b")

	forward := func(user string, seq int64, channel string) int {
		body := fmt.Sprintf(`{"text":"","channelId":%q,"forwardOf":{"roomId":"room-1","seq":%d}}`, channel, seq)
		return statusOf(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/chat", user, body)
	}
	if c := forward("bob", secret, "ch_a"); c != http.StatusBadRequest {
		t.Fatalf("bob forwards a message he cannot read = %d", c)
	}
	if c := forward("bob", 9999, "ch_a"); c != http.StatusBadRequest {
		t.Fatalf("forwarding no message = %d", c)
	}
	if c := forward("carl", secret, "ch_a"); c != http.StatusOK {
		t.Fatalf("carl (in the section) forwards it into an open channel = %d", c)
	}
	if c := forward("bob", open, "ch_a"); c != http.StatusOK {
		t.Fatalf("bob forwards an open message = %d", c)
	}

	msgs, err := svc.Chat.(*store.MemoryChat).RoomHistory(dctx(), "room-1", "ch_a", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var fwd *wire.ChatMessagePayload
	for _, m := range msgs {
		p, _ := wire.DecodePayload[wire.ChatMessagePayload](json.RawMessage(m.Payload))
		if p.ForwardOf != nil && p.ForwardOf.Seq == secret {
			fwd = &p
		}
	}
	if fwd == nil || fwd.Text != "" || fwd.ForwardOf.ChannelID != "ch_b" {
		t.Fatalf("the stored forward = %+v (its source by reference, no text)", fwd)
	}

	src := fmt.Sprintf("%s/v1/rooms/room-1/messages/%d", srv.URL, secret)
	if c, _ := getStatus(t, src, "bob"); c != http.StatusNotFound {
		t.Fatalf("bob opens the forwarded secret = %d", c)
	}
	c, raw := getStatus(t, src, "carl")
	var got wire.FeedMessage
	_ = json.Unmarshal(raw, &got)
	if c != http.StatusOK || got.Sequence != secret || got.ChannelID != "ch_b" {
		t.Fatalf("carl opens it = %d %s", c, raw)
	}
	if c, _ := getStatus(t, src, "mallory"); c != http.StatusForbidden {
		t.Fatalf("a non-member opens it = %d", c)
	}
}
