package bridge

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// Sessions per channel, one workspace per agent
// (docs/prd/2026-10-13-sessions-per-channel.md).

func channelDrive(t *testing.T, b *Bridge, id, channel, text string) {
	t.Helper()
	d := testDrive(b, jobRef{ID: id, RequesterUserID: "maya"}, "r1", "maya", "core-a", "agent-a", "t", text, channel)
	if _, err := b.driveClaim(context.Background(), d, nil); err != nil {
		t.Errorf("%s: %v", id, err)
	}
}

// Two channels get two sessions in one workspace: a file the agent wrote
// from channel A is in the workspace channel B's session starts in, and
// each session's conversation holds only its own channel's messages.
func TestChannelsHaveSessionsOfOneWorkspace(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	c.agentWrites(map[string]string{"plan.md": "written from general"})
	channelDrive(t, b, "job-1", "general", "alpha task in general")
	channelDrive(t, b, "job-2", "test", "beta task in test")
	channelDrive(t, b, "job-3", "general", "alpha follow-up")

	created, sessions := c.snapshot()
	if len(sessions) != 2 || len(created) != 1 || sessions[0].workspace != sessions[1].workspace {
		t.Fatalf("sessions %+v, workspaces %v; want two sessions in one workspace", sessions, created)
	}
	if strings.Join(sessions[1].files, ",") != "plan.md" {
		t.Fatalf("the test channel's session started with %v; want the file written from general", sessions[1].files)
	}
	general, test := strings.Join(sessions[0].turns, "\n"), strings.Join(sessions[1].turns, "\n")
	if len(sessions[0].turns) != 2 || !strings.Contains(general, "alpha task in general") || !strings.Contains(general, "alpha follow-up") || strings.Contains(general, "beta task") {
		t.Fatalf("general's session conversation:\n%s", general)
	}
	if len(sessions[1].turns) != 1 || !strings.Contains(test, "beta task in test") || strings.Contains(test, "alpha") {
		t.Fatalf("test's session conversation:\n%s", test)
	}
}

// Turns of two channels on one workspace never overlap: the drive lock is
// the workspace's, so the second channel's turn waits for the first.
func TestChannelTurnsOnOneWorkspaceNeverOverlap(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	c.turnPolls = 2
	b := fsBridge(t, g, c, "")
	var wg sync.WaitGroup
	for i, ch := range []string{"general", "test", "general", "test"} {
		wg.Add(1)
		go func(i int, ch string) {
			defer wg.Done()
			channelDrive(t, b, "job-"+string(rune('a'+i)), ch, "task "+ch)
		}(i, ch)
	}
	wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.overlaps != 0 {
		t.Fatalf("%d turns started while another ran in the workspace", c.overlaps)
	}
	if len(c.sessions) != 2 || len(c.created) != 1 {
		t.Fatalf("sessions %d, workspaces %v; want two sessions in one workspace", len(c.sessions), c.created)
	}
}
