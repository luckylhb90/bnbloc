package realtime

import (
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func newTestRoster(publish func(wire.Event)) (*AgentRoster, *controllableTimers) {
	timers := &controllableTimers{}
	r := NewAgentRoster(publish)
	r.after = timers.after
	return r, timers
}

// Advertising publishes a snapshot containing the owner's agents; a deferred
// withdraw removes them only after the grace window elapses.
func TestRosterAdvertiseThenDeferredWithdraw(t *testing.T) {
	var events []wire.Event
	r, timers := newTestRoster(func(ev wire.Event) { events = append(events, ev) })

	r.Advertise("room-1", "alice", []AgentRef{{Core: "acme", Provider: "codex"}})
	if len(events) != 1 || events[0].Kind != "agent_roster" {
		t.Fatalf("advertise events = %v, want one agent_roster", kinds(events))
	}
	if got := rosterAgents(events[0]); len(got) != 1 || got[0].OwnerUserID != "alice" || got[0].Provider != "codex" {
		t.Fatalf("snapshot = %+v, want alice/codex", got)
	}

	r.Withdraw("room-1", "alice")
	// Withdraw is deferred: nothing emitted yet, and the agent is still listed.
	if len(events) != 1 {
		t.Fatalf("after withdraw events = %v, want no new event before grace", kinds(events))
	}
	if got := r.Snapshot("room-1"); len(got) != 1 {
		t.Fatalf("snapshot during grace = %+v, want agent still present", got)
	}

	timers.fire()
	if len(events) != 2 || events[1].Kind != "agent_roster" {
		t.Fatalf("finalized events = %v, want a second agent_roster", kinds(events))
	}
	if got := rosterAgents(events[1]); len(got) != 0 {
		t.Fatalf("finalized snapshot = %+v, want empty", got)
	}
}

// A re-advertise within the grace window cancels the pending withdraw and, when
// the agent set is unchanged, does not churn subscribers with a new snapshot.
func TestRosterReadvertiseCancelsWithdrawNoChurn(t *testing.T) {
	var events []wire.Event
	r, timers := newTestRoster(func(ev wire.Event) { events = append(events, ev) })

	r.Advertise("room-1", "alice", []AgentRef{{Core: "acme", Provider: "codex"}})
	r.Withdraw("room-1", "alice")
	// Reconnect with the same set: cancels withdraw, emits nothing (no change).
	r.Advertise("room-1", "alice", []AgentRef{{Core: "acme", Provider: "codex"}})
	if len(events) != 1 {
		t.Fatalf("events after reconnect = %v, want only the first advertise", kinds(events))
	}
	// The cancelled timer firing must not remove a live owner.
	timers.fire()
	if got := r.Snapshot("room-1"); len(got) != 1 {
		t.Fatalf("snapshot after cancelled withdraw = %+v, want agent retained", got)
	}
}

// Two owners advertise independently; the room snapshot is the union, and each
// owner's agents stay distinct (multi-user isolation).
func TestRosterUnionAcrossOwners(t *testing.T) {
	var events []wire.Event
	r, _ := newTestRoster(func(ev wire.Event) { events = append(events, ev) })

	r.Advertise("room-1", "alice", []AgentRef{{Core: "acme", Provider: "codex"}})
	r.Advertise("room-1", "bob", []AgentRef{{Core: "orbit", Provider: "claude-code"}})

	got := r.Snapshot("room-1")
	if len(got) != 2 {
		t.Fatalf("union snapshot = %+v, want two agents", got)
	}
	// Sorted by owner: alice before bob.
	if got[0].OwnerUserID != "alice" || got[0].Provider != "codex" {
		t.Fatalf("snapshot[0] = %+v, want alice/codex", got[0])
	}
	if got[1].OwnerUserID != "bob" || got[1].Provider != "claude-code" {
		t.Fatalf("snapshot[1] = %+v, want bob/claude-code", got[1])
	}
}

// A second subscription for one owner refcounts: the withdraw fires only when
// the last live reference goes.
func TestRosterRefcountDefersOnLastWithdraw(t *testing.T) {
	var events []wire.Event
	r, timers := newTestRoster(func(ev wire.Event) { events = append(events, ev) })

	r.Advertise("room-1", "alice", []AgentRef{{Provider: "codex"}})
	r.Advertise("room-1", "alice", []AgentRef{{Provider: "codex"}}) // second device
	r.Withdraw("room-1", "alice")                                   // one reference remains
	timers.fire()
	if got := r.Snapshot("room-1"); len(got) != 1 {
		t.Fatalf("snapshot with one live ref = %+v, want agent retained", got)
	}
	r.Withdraw("room-1", "alice") // last reference
	timers.fire()
	if got := r.Snapshot("room-1"); len(got) != 0 {
		t.Fatalf("snapshot after last withdraw = %+v, want empty", got)
	}
}

// normalizeAgents drops empty providers and de-dups (core,provider) regardless
// of case/whitespace, so change detection is stable.
func TestRosterNormalizeDedupesAndDropsEmpty(t *testing.T) {
	var events []wire.Event
	r, _ := newTestRoster(func(ev wire.Event) { events = append(events, ev) })

	r.Advertise("room-1", "alice", []AgentRef{
		{Core: "Acme", Provider: "Codex"},
		{Core: "acme", Provider: "codex"}, // duplicate after normalization
		{Core: "acme", Provider: ""},      // dropped (no provider)
	})
	got := r.Snapshot("room-1")
	if len(got) != 1 || got[0].Core != "acme" || got[0].Provider != "codex" {
		t.Fatalf("normalized snapshot = %+v, want single acme/codex", got)
	}
}

func rosterAgents(ev wire.Event) []wire.RosterAgent {
	payload, ok := ev.Payload.(map[string]any)
	if !ok {
		return nil
	}
	agents, ok := payload["agents"].([]wire.RosterAgent)
	if !ok {
		return nil
	}
	return agents
}
