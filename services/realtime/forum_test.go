package realtimehttp

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A top-level post in a forum channel (channels PRD §2.1c) is the root of a
// new thread, titled and tagged (only the channel's own tags), and the answer
// names it; a post inside the thread opens nothing. When the channel's agents
// answer every new thread (C19), the default agent is asked once in the
// thread, as the opener, marked automatic.
func TestForumPostOpensItsThreadAndAsksTheAgent(t *testing.T) {
	srv, svc := newChannelTestServer(t)
	settings := wire.ChannelSettings{
		PostingStyle:       wire.ChannelPostThreads,
		Tags:               []wire.ChannelTag{{ID: "bug", Name: "Bug"}},
		AgentParticipation: wire.ChannelAgentsThreads,
		DefaultAgent:       &wire.ChannelAgent{OwnerID: "alice", Core: "acme", Provider: "codex"},
	}
	if err := svc.Channels.(*store.MemoryChannel).UpdateChannel(dctx(), "room-1", "ch_a", store.ChannelPatch{Settings: &settings}); err != nil {
		t.Fatal(err)
	}

	resp := channelPost(t, srv, "/v1/rooms/room-1/chat", "bob",
		`{"text":"Login fails on Safari","channelId":"ch_a","clientMsgId":"f1","threadTitle":"Safari login","threadTags":["bug","nope","bug"]}`)
	raw, _ := io.ReadAll(resp.Body)
	var reply wire.ChatPostReply
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &reply) != nil || reply.ThreadID == "" {
		t.Fatalf("forum post = %d %s, want the thread named", resp.StatusCode, raw)
	}
	th, found, _ := svc.Channels.(*store.MemoryChannel).GetChannel(dctx(), "room-1", reply.ThreadID)
	if !found || !th.IsThread() || th.ParentID != "ch_a" || th.RootSeq != reply.Sequence || th.Name != "Safari login" ||
		len(th.Thread.Tags) != 1 || th.Thread.Tags[0] != "bug" {
		t.Fatalf("the post's thread = %+v (found %v)", th, found)
	}

	// The default agent was asked in the thread, as the opener, marked automatic.
	var hist struct {
		Messages []struct {
			Payload map[string]any `json:"payload"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(channelGet(t, srv, "/v1/rooms/room-1/history?channelId="+reply.ThreadID, "bob")), &hist); err != nil {
		t.Fatal(err)
	}
	if len(hist.Messages) != 1 {
		t.Fatalf("thread history = %+v, want the one request to the agent", hist.Messages)
	}
	p := hist.Messages[0].Payload
	rcpt, _ := p["recipient"].(map[string]any)
	if p[wire.MessageAgentAuto] != true || p["userId"] != "bob" || rcpt["ownerId"] != "alice" || rcpt["provider"] != "codex" || p["text"] != "Login fails on Safari" {
		t.Fatalf("the automatic request = %+v", p)
	}

	// A post in the thread is a reply, not another thread.
	resp = channelPost(t, srv, "/v1/rooms/room-1/chat", "bob", `{"text":"also on iPad","channelId":"`+reply.ThreadID+`","clientMsgId":"f2"}`)
	raw, _ = io.ReadAll(resp.Body)
	var inner wire.ChatPostReply
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &inner) != nil || inner.ThreadID != "" {
		t.Fatalf("a post in the thread = %d %s, want no new thread", resp.StatusCode, raw)
	}
	// A post in an ordinary channel opens nothing either.
	resp = channelPost(t, srv, "/v1/rooms/room-1/chat", "bob", `{"text":"hi","channelId":"ch_b","clientMsgId":"f3"}`)
	raw, _ = io.ReadAll(resp.Body)
	var plain wire.ChatPostReply
	if json.Unmarshal(raw, &plain) != nil || plain.ThreadID != "" {
		t.Fatalf("a post in an ordinary channel = %s", raw)
	}
}
