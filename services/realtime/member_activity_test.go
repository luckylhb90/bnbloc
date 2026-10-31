package realtimehttp

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func activityPayload[T any](t *testing.T, f sseFrame) T {
	t.Helper()
	var ev struct {
		Payload T `json:"payload"`
	}
	if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
		t.Fatalf("decode %s: %v", f.data, err)
	}
	return ev.Payload
}

// The member's activity stream sends the unread state of the rooms they
// are in (none of the others), then what changes: a message posted through
// another replica, a message deleted, a mark of read on any device.
func TestMemberActivityStream(t *testing.T) {
	c := newCluster(t, 2)
	a, b := c.reps[0], c.reps[1]
	c.members.Add("room-1", "maya")
	c.members.Add("room-1", "jack")
	c.members.Add("room-2", "maya")
	c.members.Add("room-3", "jack") // not maya's
	c.chatVia(b, "room-1", "jack", "before", "m-0")

	s := c.open(a, "/v1/my/activity?rooms=room-1,room-2,room-3", "maya")
	st := activityPayload[wire.UnreadReply](t, s.next(t, wire.UnreadStateKind))
	if st.Unread["room-1"] != 1 || st.Unread["room-2"] != 0 || st.Unread["room-3"] != 0 {
		t.Fatalf("state %+v, want room-1: 1 and nothing of room-3", st.Unread)
	}

	seq := c.chatVia(b, "room-1", "jack", "hello", "m-1")
	c.chatVia(b, "room-3", "jack", "elsewhere", "m-3")
	d := activityPayload[wire.UnreadDeltaPayload](t, s.next(t, wire.UnreadDeltaKind))
	if got := d.Rooms["room-1"].Seqs; !slices.Equal(got, []int64{seq}) {
		t.Fatalf("delta %+v, want room-1 seq %d", d.Rooms, seq)
	}
	if _, leaked := d.Rooms["room-3"]; leaked {
		t.Fatal("a room the member is not in reached their stream")
	}

	resp, err := http.DefaultClient.Do(c.request(b, http.MethodPost, "/v1/rooms/room-1/read", "maya", map[string]any{"seq": seq}))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if d := activityPayload[wire.UnreadDeltaPayload](t, s.next(t, wire.UnreadDeltaKind)); !d.Rooms["room-1"].Read {
		t.Fatalf("after a mark of read through another replica: %+v, want room-1 read", d.Rooms)
	}

	resp, err = http.DefaultClient.Do(c.request(a, http.MethodDelete, "/v1/rooms/room-1/messages/1", "jack", nil))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if d := activityPayload[wire.UnreadDeltaPayload](t, s.next(t, wire.UnreadDeltaKind)); !slices.Equal(d.Rooms["room-1"].Deleted, []int64{1}) {
		t.Fatalf("after a delete: %+v, want room-1 deleted 1", d.Rooms)
	}
}
