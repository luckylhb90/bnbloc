package bridge

import (
	"context"
	"strings"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// docs/prd/2026-10-22-session-continuation.md: a core that restarted and kept the
// conversation says so, and the bridge neither tells the room the agent forgot nor
// hands the conversation over again.

const handoverMarker = "you do not remember it"

// continueDrive runs one turn of agent-a in channel ch as message seq.
func continueDrive(t *testing.T, b *Bridge, id string, seq int64, ch string) {
	t.Helper()
	d := testDrive(b, jobRef{ID: id, RequesterUserID: "maya", MessageSeq: seq}, "r1", "maya", "core-a", "agent-a", "t", "go "+id, ch)
	if _, err := b.driveClaim(context.Background(), d, nil); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
}

// historyOf seeds a channel: maya spoke, then the agent answered.
func historyOf(g *pullGateway, ch string, seq int64) {
	g.addChatRow(ch, seq, "maya", "go before")
	g.addAgentRow(ch, seq+1, "maya", "agent-a", "answered before")
}

func resetNotices(g *pullGateway) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, r := range g.reports {
		if strings.Contains(r, `"`+wire.NoticeContextReset+`"`) {
			n++
		}
	}
	return n
}

// The case this record is about: the core restarted, kept the conversation.
func TestAKeptConversationIsContinuedWithoutANoticeOrAHandover(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	continueDrive(t, b, "job-1", 1, "general")
	historyOf(g, "general", 1)
	_, sessions := c.snapshot()
	c.mu.Lock()
	c.continuable = map[string]bool{sessions[0].id: true}
	c.mu.Unlock()

	continueDrive(t, b, "job-2", 3, "general")

	_, sessions = c.snapshot()
	if len(sessions) != 2 || sessions[1].continues != sessions[0].id {
		t.Fatalf("sessions = %+v; want a second one continuing %s", sessions, sessions[0].id)
	}
	if n := resetNotices(g); n != 0 {
		t.Fatalf("the room was told %d times that the agent forgot; it did not", n)
	}
	if strings.Contains(sessions[1].turns[0], handoverMarker) {
		t.Fatalf("a continued conversation was handed over again:\n%s", sessions[1].turns[0])
	}
}

// The same situation without the core's word is today's path: the room is told,
// and the conversation is handed over. It is also what makes the test above prove
// something: the history it seeds does produce a handover.
func TestALostSessionStillGetsTheNoticeAndTheHandover(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	continueDrive(t, b, "job-1", 1, "general")
	historyOf(g, "general", 1)
	_, sessions := c.snapshot()
	c.mu.Lock()
	c.stale[sessions[0].id] = true
	c.mu.Unlock()

	continueDrive(t, b, "job-2", 3, "general")

	_, sessions = c.snapshot()
	if len(sessions) != 2 || sessions[1].continues != "" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if resetNotices(g) != 1 || !strings.Contains(sessions[1].turns[0], handoverMarker) {
		t.Fatalf("notices %d, handover %v; want the notice and the handover", resetNotices(g), strings.Contains(sessions[1].turns[0], handoverMarker))
	}
}

// A conversation the core cannot hand over after all: nothing ran, so the turn is a
// new session the old way, with the room told once.
func TestAContinuationTheCoreRefusesFallsBackVisibly(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	continueDrive(t, b, "job-1", 1, "general")
	historyOf(g, "general", 1)
	_, sessions := c.snapshot()
	c.mu.Lock()
	c.continuable = map[string]bool{sessions[0].id: true}
	c.lostContinuation = true
	c.mu.Unlock()

	continueDrive(t, b, "job-2", 3, "general")

	_, sessions = c.snapshot()
	if len(sessions) != 2 || sessions[1].continues != "" {
		t.Fatalf("sessions = %+v; want one new session that continues nothing", sessions)
	}
	if resetNotices(g) != 1 || !strings.Contains(sessions[1].turns[0], handoverMarker) {
		t.Fatalf("notices %d, handover %v; want one notice and the handover", resetNotices(g), strings.Contains(sessions[1].turns[0], handoverMarker))
	}
}

// A core that says "continuable" and then creates a session that took nothing over
// broke the contract: the agent may remember none of it, so the room is told.
func TestACoreThatDoesNotContinueWhatItPromisedIsReported(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	continueDrive(t, b, "job-1", 1, "general")
	historyOf(g, "general", 1)
	_, sessions := c.snapshot()
	c.mu.Lock()
	c.continuable = map[string]bool{sessions[0].id: true}
	c.noContinued = true
	c.mu.Unlock()

	continueDrive(t, b, "job-2", 3, "general")

	waitFor(t, "the notice", func() bool { return resetNotices(g) == 1 })
}

// A workspace has a session per channel: each channel's session continues its own
// conversation, never another channel's (the core adopts by the id it is given).
func TestEachChannelContinuesItsOwnConversation(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	continueDrive(t, b, "job-1", 1, "general")
	continueDrive(t, b, "job-2", 1, "design")
	_, sessions := c.snapshot()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v", sessions)
	}
	general, design := sessions[0].id, sessions[1].id
	c.mu.Lock()
	c.continuable = map[string]bool{general: true, design: true}
	c.mu.Unlock()

	// The channel that wrote last asks last, as a restart may order them.
	continueDrive(t, b, "job-4", 3, "design")
	continueDrive(t, b, "job-3", 3, "general")

	_, sessions = c.snapshot()
	if len(sessions) != 4 || sessions[2].continues != design || sessions[3].continues != general {
		t.Fatalf("sessions = %+v; want design to continue %s and general %s", sessions, design, general)
	}
	if resetNotices(g) != 0 {
		t.Fatalf("a notice was posted for a continued conversation")
	}
}

// The next turn of a continued session is an ordinary reused one: no probe loop, no
// second session.
func TestAContinuedSessionIsThenReused(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	continueDrive(t, b, "job-1", 1, "general")
	_, sessions := c.snapshot()
	c.mu.Lock()
	c.continuable = map[string]bool{sessions[0].id: true}
	c.mu.Unlock()
	continueDrive(t, b, "job-2", 2, "general")
	continueDrive(t, b, "job-3", 3, "general")

	_, sessions = c.snapshot()
	if len(sessions) != 2 || len(sessions[1].turns) != 2 {
		t.Fatalf("sessions = %+v; want the continued session reused for the third turn", sessions)
	}
}

// The successor starts from what the session it replaces had been told: it hears
// what others said while the core was down, not the lines the agent already took in.
func TestAContinuedSessionKeepsWhatItHadBeenTold(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	continueDrive(t, b, "job-1", 1, "general")
	g.addChatRow("general", 1, "jack", "said before the restart")
	continueDrive(t, b, "job-2", 2, "general") // a reused turn: it takes the line in
	g.addChatRow("general", 3, "jack", "said while the core was down")
	_, sessions := c.snapshot()
	c.mu.Lock()
	c.continuable = map[string]bool{sessions[0].id: true}
	c.mu.Unlock()

	continueDrive(t, b, "job-3", 4, "general")

	_, sessions = c.snapshot()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %+v", sessions)
	}
	prompt := sessions[1].turns[0]
	if !strings.Contains(prompt, "said while the core was down") {
		t.Fatalf("the continued session was not told what was said meanwhile:\n%s", prompt)
	}
	if strings.Contains(prompt, "said before the restart") {
		t.Fatalf("the continued session was told again what it had taken in:\n%s", prompt)
	}
}

// The replies of a continued session say whose conversation it took over, so the
// release gate can follow the chain; a session that continued nothing says nothing.
func TestRepliesOfAContinuedSessionNameItsPredecessor(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	post := func(rs *roomSession) {
		if err := b.postRoomMessage(context.Background(), "r1", rs, corepkg.Message{MessageID: "m", Text: "hi", Role: "assistant"}, "general"); err != nil {
			t.Fatal(err)
		}
	}
	post(&roomSession{owner: "maya", provider: "agent-a", core: "core-a", sessionID: "s2", continued: "s1"})
	post(&roomSession{owner: "maya", provider: "agent-a", core: "core-a", sessionID: "s3"})

	g.mu.Lock()
	defer g.mu.Unlock()
	var withIt, without int
	for _, r := range g.reports {
		switch {
		case strings.Contains(r, `"continues":"s1"`):
			withIt++
		case strings.Contains(r, `"text":"hi"`):
			without++
		}
	}
	if withIt != 1 || without != 1 {
		t.Fatalf("reports = %q; want one reply naming s1 and one naming nothing", g.reports)
	}
}
