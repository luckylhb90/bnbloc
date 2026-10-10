package realtime

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The sandboxed flag rides the advertisement into the snapshot, and flipping
// it is a roster change (subscribers see the shield badge appear/disappear).
func TestRosterSandboxedFlag(t *testing.T) {
	var events []wire.Event
	r, _ := newTestRoster(func(ev wire.Event) { events = append(events, ev) })

	r.Advertise("room-1", "alice", []AgentRef{{Core: "c", Provider: "claude", Sandboxed: true}, {Core: "c", Provider: "codex"}})
	got := r.Snapshot("room-1")
	if len(got) != 2 || !got[0].Sandboxed || got[1].Sandboxed {
		t.Fatalf("snapshot = %+v, want claude sandboxed, codex not", got)
	}
	r.Advertise("room-1", "alice", []AgentRef{{Core: "c", Provider: "claude"}, {Core: "c", Provider: "codex"}})
	if len(events) != 2 || rosterAgents(events[1])[0].Sandboxed {
		t.Fatalf("un-sandboxing must republish: %d events, last %+v", len(events), rosterAgents(events[len(events)-1]))
	}
	// Duplicates after normalization keep the flag.
	r.Advertise("room-2", "bob", []AgentRef{{Core: "C", Provider: "Claude"}, {Core: "c", Provider: "claude", Sandboxed: true}})
	if got := r.Snapshot("room-2"); len(got) != 1 || !got[0].Sandboxed {
		t.Fatalf("dedup snapshot = %+v", got)
	}
}
