package realtimehttp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func markCall(t *testing.T, base, method, user string, seq int64, body string) int {
	t.Helper()
	return statusOf(t, method, fmt.Sprintf("%s/v1/rooms/room-1/messages/%d/marks", base, seq), user, body)
}

func listMarks(t *testing.T, base, user string) []wire.MessageMark {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/rooms/room-1/marks", nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("marks as %s = %d (%s)", user, resp.StatusCode, raw)
	}
	var out wire.MessageMarks
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Marks
}

func kinds(ms []wire.MessageMark) string {
	var out []string
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%d:%s:%s:%s", m.Seq, m.Kind, m.UserID, m.Value))
	}
	return strings.Join(out, ",")
}

// Reactions are any poster's, seen by the channel's readers; a pin is put
// by those who manage the channel; a save is the member's own and seen by
// nobody else; a message of a members-only channel does not exist for a
// member outside its section; a visitor reacts to nothing; a deleted message
// takes its marks with it; the distinct reactions on one message are bounded.
func TestMessageMarks(t *testing.T) {
	srv, svc, _ := newMembersOnlyServer(t)
	svc.Marks = store.NewMemoryMessageMarks()
	mem := svc.Membership.(*membership.Static)
	mem.AddRole("room-1", "erin", store.RoleAdmin)
	mem.AddRole("room-1", "vic", store.RoleVisitor)

	open := postChat(t, srv, "alice", "ship on friday?", "ch_a")
	secret := postChat(t, srv, "alice", "the secret plan", "ch_b")

	if c := markCall(t, srv.URL, http.MethodPut, "bob", open, `{"kind":"reaction","value":"+1"}`); c != http.StatusOK {
		t.Fatalf("bob reacts = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "bob", open, `{"kind":"reaction","value":"+1"}`); c != http.StatusOK {
		t.Fatalf("the same reaction again = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "bob", open, `{"kind":"reaction","value":"two words"}`); c != http.StatusBadRequest {
		t.Fatalf("a reaction that is not one emoji = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "vic", open, `{"kind":"reaction","value":"+1"}`); c != http.StatusForbidden {
		t.Fatalf("a visitor reacts = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "bob", open, `{"kind":"pin"}`); c != http.StatusForbidden {
		t.Fatalf("bob (no manager of ch_a) pins = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "erin", open, `{"kind":"pin"}`); c != http.StatusOK {
		t.Fatalf("an admin pins = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "bob", open, `{"kind":"saved"}`); c != http.StatusOK {
		t.Fatalf("bob saves = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "bob", secret, `{"kind":"reaction","value":"+1"}`); c != http.StatusNotFound {
		t.Fatalf("bob reacts to a members-only message = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "carl", secret, `{"kind":"reaction","value":"eyes"}`); c != http.StatusOK {
		t.Fatalf("carl (in the section) reacts = %d", c)
	}

	if got := kinds(listMarks(t, srv.URL, "bob")); got != fmt.Sprintf("%d:reaction:bob:+1,%d:pin::,%d:saved:bob:", open, open, open) {
		t.Fatalf("bob's marks = %s", got)
	}
	if got := kinds(listMarks(t, srv.URL, "alice")); strings.Contains(got, "saved") || !strings.Contains(got, fmt.Sprintf("%d:reaction:carl:eyes", secret)) {
		t.Fatalf("alice's marks = %s (no one else's save; the section's reaction)", got)
	}

	if c := markCall(t, srv.URL, http.MethodDelete, "bob", open, `{"kind":"reaction","value":"+1"}`); c != http.StatusNoContent {
		t.Fatalf("bob takes his reaction off = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodDelete, "alice", open, `{"kind":"reaction","value":"+1"}`); c != http.StatusNoContent {
		t.Fatalf("taking off a reaction one never put = %d", c)
	}

	busy := postChat(t, srv, "alice", "react to me", "ch_a")
	for i := 0; i < wire.MaxMessageReactions; i++ {
		if c := markCall(t, srv.URL, http.MethodPut, "bob", busy, fmt.Sprintf(`{"kind":"reaction","value":"e%d"}`, i)); c != http.StatusOK {
			t.Fatalf("reaction %d = %d", i, c)
		}
	}
	if c := markCall(t, srv.URL, http.MethodPut, "alice", busy, `{"kind":"reaction","value":"one-more"}`); c != http.StatusConflict {
		t.Fatalf("a reaction past the bound = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "alice", busy, `{"kind":"reaction","value":"e0"}`); c != http.StatusOK {
		t.Fatalf("joining an existing reaction past the bound = %d", c)
	}

	if c := statusOf(t, http.MethodDelete, fmt.Sprintf("%s/v1/rooms/room-1/messages/%d", srv.URL, open), "alice", ""); c != http.StatusOK {
		t.Fatalf("delete = %d", c)
	}
	for _, m := range listMarks(t, srv.URL, "bob") {
		if m.Seq == open {
			t.Fatalf("a deleted message keeps its mark %+v", m)
		}
	}
}

// A reaction is announced on its message's channel; a save is not announced.
func TestMessageMarkedEvent(t *testing.T) {
	srv, svc := newChannelTestServer(t)
	svc.Marks = store.NewMemoryMessageMarks()
	seq := postChat(t, srv, "alice", "hello", "ch_a")
	events := openPeopleTestStream(t, srv, svc, "alice", "allChannels=1&replay=0", 1)
	if c := markCall(t, srv.URL, http.MethodPut, "bob", seq, `{"kind":"saved"}`); c != http.StatusOK {
		t.Fatalf("save = %d", c)
	}
	if c := markCall(t, srv.URL, http.MethodPut, "bob", seq, `{"kind":"reaction","value":"tada"}`); c != http.StatusOK {
		t.Fatalf("react = %d", c)
	}
	var marked wire.Event
	collectUntil(t, events, func(ev wire.Event) bool {
		if ev.Kind == wire.EventMessageMarked {
			marked = ev
			return true
		}
		return false
	})
	p, ok := wire.DecodePayload[wire.MessageMarkedPayload](marked.Payload)
	if !ok {
		t.Fatalf("payload = %+v", marked.Payload)
	}
	if marked.ChannelID != "ch_a" || p.Mark.Kind != wire.MarkReaction || p.Mark.Value != "tada" {
		t.Fatalf("the first announced mark = %+v %+v (a save is never announced)", marked, p)
	}
}
