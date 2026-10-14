package realtimehttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The revisions listing names every message edited or deleted after a
// time, oldest first, so agents' records follow the room; an agent host's
// omits the people chat's.
func TestRevisionsListEditsAndDeletes(t *testing.T) {
	srv, svc, people := newPeopleChatServer(t)
	if err := svc.Membership.(*membership.Static).PutAgentHost(t.Context(), "", "room-1", "root"); err != nil {
		t.Fatal(err)
	}
	edited := postChat(t, srv, "alice", "draft", "ch_a")
	secret := postChat(t, srv, "alice", "private", people)
	postChat(t, srv, "alice", "untouched", "ch_a")
	for _, c := range []struct {
		method string
		seq    int64
		body   string
	}{{http.MethodPatch, edited, `{"text":"final"}`}, {http.MethodDelete, secret, ""}} {
		req, _ := http.NewRequest(c.method, fmt.Sprintf("%s/v1/rooms/room-1/messages/%d", srv.URL, c.seq), strings.NewReader(c.body))
		req.Header.Set("X-Bnbloc-User", "alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s %d = %d", c.method, c.seq, resp.StatusCode)
		}
	}
	page := func(user, query string) wire.RevisionsPage {
		var p wire.RevisionsPage
		if err := json.Unmarshal([]byte(channelGet(t, srv, "/v1/rooms/room-1/history/revisions"+query, user)), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	all := page("alice", "")
	if len(all.Revisions) != 2 || all.Revisions[0].Sequence != edited || all.Revisions[0].Deleted ||
		all.Revisions[1].Sequence != secret || !all.Revisions[1].Deleted || all.LatestMs != all.Revisions[1].AtMs {
		t.Fatalf("revisions = %+v", all)
	}
	if host := page("root", ""); len(host.Revisions) != 1 || host.Revisions[0].Sequence != edited || host.Revisions[0].ChannelID != "ch_a" {
		t.Fatalf("an agent host's revisions = %+v; want the people chat's omitted", host)
	}
	if later := page("alice", fmt.Sprintf("?after=%d", all.LatestMs)); len(later.Revisions) != 0 || later.LatestMs != all.LatestMs {
		t.Fatalf("revisions after the newest = %+v", later)
	}
}
