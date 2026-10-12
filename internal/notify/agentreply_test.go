package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestReplySummary(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Plain answer.", "Plain answer."},
		{"# Fixed\n\nThe **build** now _passes_; see [the log](https://ci/1) and `go test`.", "Fixed The build now passes; see the log and go test."},
		{"Here is the patch:\n\n```go\nfunc main() {}\n```\n\nDone.", "Here is the patch: [code] Done."},
		{"- one\n- two\n1. three\n> quoted\n\n---\n![diagram](x.png)", "one two three quoted diagram"},
		{"| a | b |\n|---|---|\n| 1 | 2 |", "a b 1 2"},
		{"<details>hidden</details> snake_case_name stays", "hidden snake_case_name stays"},
		{"```\nunterminated code", "[code]"},
	}
	for _, c := range cases {
		if got := ReplySummary(c.in, 0); got != c.want {
			t.Errorf("ReplySummary(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := strings.Repeat("word ", 100)
	got := ReplySummary(long, ReplySummaryRunes)
	if r := []rune(got); len(r) > ReplySummaryRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("long summary = %d runes %q", len(r), got)
	}
	if got := ReplySummary(strings.Repeat("界", 400), 10); len([]rune(got)) != 10 {
		t.Fatalf("rune-safe cut = %q", got)
	}
}

func TestMessageURL(t *testing.T) {
	cases := []struct {
		pub, room, channel string
		seq                int64
		want               string
	}{
		{"https://b.example/", "r1", "c 2", 42, "https://b.example/#/room/r1?seq=42&channel=c+2"},
		{"https://b.example", "r1", "", 42, "https://b.example/#/room/r1?seq=42"},
		{"https://b.example", "r1", "c1", 0, "https://b.example/#/room/r1"},
		{"", "r1", "c1", 42, ""},
	}
	for _, c := range cases {
		if got := MessageURL(c.pub, c.room, c.channel, c.seq); got != c.want {
			t.Errorf("MessageURL(%q,%q,%q,%d) = %q, want %q", c.pub, c.room, c.channel, c.seq, got, c.want)
		}
	}
	if got := urlHash("https://b.example/#/room/r1?seq=4", "r1"); got != "#/room/r1?seq=4" {
		t.Fatalf("urlHash = %q", got)
	}
	if got := urlHash("", "r 1"); got != "#/room/r%201" {
		t.Fatalf("urlHash fallback = %q", got)
	}
}

// drain processes every queued observation on the test goroutine.
func drain(o *Observer) {
	for len(o.queue) > 0 {
		o.process(context.Background(), <-o.queue)
	}
}

func replyEvent(session string, seq int64, atMs int64, texts ...string) wire.Event {
	msgs := []any{}
	for _, tx := range texts {
		msgs = append(msgs, map[string]any{"seq": 1, "payload": map[string]any{"role": "assistant", "text": tx, "ownerUserId": "bob", "provider": "codex"}})
	}
	return wire.Event{RoomID: "r1", ChannelID: "c1", Kind: "agent_messages", Sequence: seq, AtUnixMs: atMs,
		Payload: map[string]any{"workspaceId": "r1", "agentSessionId": session, "channelId": "c1", "messages": msgs}}
}

func repliedJob(jobID, session string, startedAtMs int64) wire.Event {
	return wire.Event{RoomID: "r1", ChannelID: "c1", Kind: "agent_job_updated", Payload: map[string]any{
		"jobId": jobID, "requesterUserId": "alice", "ownerUserId": "bob", "provider": "codex", "channelId": "c1",
		"messageSeq": 10, "messageText": "fix the build", "status": "replied", "startedAtMs": startedAtMs,
		"core": "acme", "replySessionId": session,
	}}
}

func TestObserverDeepLinksAndReplySummary(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	o := NewObserver(f.emitter, fakeMembership{"r1/bob": true}, 16)
	now := time.Now().UnixMilli()

	// A mention links to the mentioning message in its channel.
	o.Observe(tctx(), wire.Event{RoomID: "r1", ChannelID: "c1", Sequence: 7, Kind: "chat_message", Payload: map[string]any{"userId": "alice", "text": "@bob look"}})
	// The reply arrives first (the bridge posts it before reporting the job
	// replied); a job notice under the job's notice session is not a reply.
	o.Observe(tctx(), replyEvent("sess-1", 11, now, "Working on it", "## Done\n\nThe **build** passes.\n\n```sh\nmake\n```"))
	o.Observe(tctx(), replyEvent(wire.JobNoticeSessionID("job_x"), 12, now, "notice"))
	o.Observe(tctx(), repliedJob("job_1", "sess-1", now-1000))
	drain(o)

	m, ok := f.payload(t, wire.NotificationMention, "msg:r1:7", "n1")
	if !ok || m.URL != "https://bnbloc.example.com/#/room/r1?seq=7&channel=c1" {
		t.Fatalf("mention = %+v ok=%v", m, ok)
	}
	fin, ok := f.payload(t, wire.NotificationAgentFinished, "job:job_1", "n1")
	if !ok {
		t.Fatal("agent_finished not queued")
	}
	if !strings.HasPrefix(fin.Text, "Done The build passes. [code]") || !strings.Contains(fin.Text, "Request: fix the build") {
		t.Fatalf("agent_finished text = %q", fin.Text)
	}
	if fin.URL != "https://bnbloc.example.com/#/room/r1?seq=11&channel=c1" {
		t.Fatalf("agent_finished url = %q", fin.URL)
	}
	if len(o.replies) != 0 || len(o.pending) != 0 {
		t.Fatalf("reply not consumed: replies=%d pending=%d", len(o.replies), len(o.pending))
	}
}

// A reply posted after the job's replied report completes the parked job.
func TestObserverParksJobUntilReplyArrives(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	o := NewObserver(f.emitter, fakeMembership{}, 16)
	now := time.Now().UnixMilli()

	o.Observe(tctx(), repliedJob("job_2", "sess-2", now))
	drain(o)
	if _, ok := f.payload(t, wire.NotificationAgentFinished, "job:job_2", "n1"); ok {
		t.Fatal("agent_finished emitted before its reply")
	}
	o.Observe(tctx(), replyEvent("sess-2", 21, now+50, "All green."))
	drain(o)
	fin, ok := f.payload(t, wire.NotificationAgentFinished, "job:job_2", "n1")
	if !ok || !strings.HasPrefix(fin.Text, "All green.") || !strings.Contains(fin.URL, "seq=21") {
		t.Fatalf("agent_finished = %+v ok=%v", fin, ok)
	}
}

// A replied job whose reply never arrives is emitted without a summary once
// the wait lapses, linking to the request; an older turn's unclaimed reply
// is never taken for this job's answer.
func TestObserverEmitsWithoutReplyAfterWait(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	o := NewObserver(f.emitter, fakeMembership{}, 16)
	now := time.Now().UnixMilli()

	o.Observe(tctx(), replyEvent("sess-3", 5, now-time.Hour.Milliseconds(), "stale answer"))
	o.Observe(tctx(), repliedJob("job_3", "sess-3", now))
	drain(o)
	o.flushPending(context.Background(), time.Now())
	if _, ok := f.payload(t, wire.NotificationAgentFinished, "job:job_3", "n1"); ok {
		t.Fatal("emitted before the wait lapsed")
	}
	o.flushPending(context.Background(), time.Now().Add(replyWait+time.Second))
	fin, ok := f.payload(t, wire.NotificationAgentFinished, "job:job_3", "n1")
	if !ok || strings.Contains(fin.Text, "stale") || fin.Text != "Request: fix the build" || !strings.Contains(fin.URL, "seq=10&channel=c1") {
		t.Fatalf("agent_finished = %+v ok=%v", fin, ok)
	}

	// A failed job keeps its reason and links to the request.
	o.Observe(tctx(), wire.Event{RoomID: "r1", Kind: "agent_job_updated", Payload: store.AgentJob{
		JobID: "job_4", RequesterUserID: "alice", OwnerUserID: "bob", Provider: "codex", ChannelID: "c9", MessageSeq: 33,
		MessageText: "deploy", Status: store.AgentJobFailed, FailureReason: store.FailureNoBridgeOnline,
	}})
	drain(o)
	failed, ok := f.payload(t, wire.NotificationAgentFailed, "job:job_4", "n1")
	if !ok || !strings.HasPrefix(failed.Text, store.FailureNoBridgeOnline.Text("codex")) || !strings.HasSuffix(failed.URL, "#/room/r1?seq=33&channel=c9") {
		t.Fatalf("agent_failed = %+v ok=%v", failed, ok)
	}
}
