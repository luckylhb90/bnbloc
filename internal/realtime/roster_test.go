package realtime

import (
	"strings"
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

	sub := r.Advertise("room-1", "alice", []AgentRef{{Core: "acme", Provider: "codex"}})
	if len(events) != 1 || events[0].Kind != "agent_roster" {
		t.Fatalf("advertise events = %v, want one agent_roster", kinds(events))
	}
	if got := rosterAgents(events[0]); len(got) != 1 || got[0].OwnerUserID != "alice" || got[0].Provider != "codex" {
		t.Fatalf("snapshot = %+v, want alice/codex", got)
	}

	r.Withdraw(sub)
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

	r.Withdraw(r.Advertise("room-1", "alice", []AgentRef{{Core: "acme", Provider: "codex"}}))
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

	first := r.Advertise("room-1", "alice", []AgentRef{{Provider: "codex"}})
	second := r.Advertise("room-1", "alice", []AgentRef{{Provider: "codex"}}) // second device
	r.Withdraw(first)                                                         // one reference remains
	timers.fire()
	if got := r.Snapshot("room-1"); len(got) != 1 {
		t.Fatalf("snapshot with one live ref = %+v, want agent retained", got)
	}
	r.Withdraw(second) // last reference
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

// Two live subscriptions of one owner with different lists (the bridge
// re-subscribed after its list changed; the older stream is still closing, or
// its retry lands late): the room shows the NEWEST subscription's list, and
// closing either one, in either order, never leaves an older list behind
// (docs/bugs/2026-11-03-room-roster-stuck-on-a-short-list.md).
func TestRosterNewestSubscriptionWinsInEitherOrder(t *testing.T) {
	short := []AgentRef{{Core: "acp", Provider: "kilo-sandbox"}}
	full := []AgentRef{{Core: "acp", Provider: "kilo-sandbox"}, {Core: "acp", Provider: "opencode"}}
	providers := func(r *AgentRoster) string {
		var p []string
		for _, a := range r.Snapshot("room-1") {
			p = append(p, a.Provider)
		}
		return strings.Join(p, ",")
	}
	for _, c := range []struct {
		name  string
		steps func(r *AgentRoster)
		want  string
	}{
		{"the short stream closes after the full one opened", func(r *AgentRoster) {
			old := r.Advertise("room-1", "me", short)
			r.Advertise("room-1", "me", full)
			r.Withdraw(old)
		}, "kilo-sandbox,opencode"},
		{"the full stream opens after the short one closed", func(r *AgentRoster) {
			r.Withdraw(r.Advertise("room-1", "me", short))
			r.Advertise("room-1", "me", full)
		}, "kilo-sandbox,opencode"},
		{"an older stream's late advertisement does not overwrite the newer one (the stuck room)", func(r *AgentRoster) {
			gone := r.Advertise("room-1", "me", full) // before the core restart
			newer := r.Advertise("room-1", "me", full)
			r.Withdraw(gone)
			late := r.Advertise("room-1", "me", short) // a retry from before the restart, landing now
			_ = newer
			r.Withdraw(late) // it closes when the bridge sees it is stale
		}, "kilo-sandbox,opencode"},
		{"both live: the newer one is the owner's list", func(r *AgentRoster) {
			r.Advertise("room-1", "me", short)
			r.Advertise("room-1", "me", full)
		}, "kilo-sandbox,opencode"},
		{"a newer short list replaces a full one (an agent really went)", func(r *AgentRoster) {
			old := r.Advertise("room-1", "me", full)
			r.Advertise("room-1", "me", short)
			r.Withdraw(old)
		}, "kilo-sandbox"},
		{"the newer stream closes first: the older one's list is all that is live", func(r *AgentRoster) {
			r.Advertise("room-1", "me", short)
			r.Withdraw(r.Advertise("room-1", "me", full))
		}, "kilo-sandbox"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _ := newTestRoster(nil)
			c.steps(r)
			if got := providers(r); got != c.want {
				t.Fatalf("roster = %s, want %s", got, c.want)
			}
		})
	}
}

// A withdrawn subscription's handle does nothing a second time, and an
// unknown one is ignored.
func TestRosterWithdrawIsOnceAndByHandle(t *testing.T) {
	r, timers := newTestRoster(nil)
	a := r.Advertise("room-1", "me", []AgentRef{{Provider: "codex"}})
	b := r.Advertise("room-1", "me", []AgentRef{{Provider: "codex"}})
	r.Withdraw(a)
	r.Withdraw(a)
	r.Withdraw(RosterSub{room: "room-1", owner: "me", seq: 999})
	timers.fire()
	if got := r.Snapshot("room-1"); len(got) != 1 {
		t.Fatalf("a repeated withdraw removed a live subscription: %+v", got)
	}
	r.Withdraw(b)
	timers.fire()
	if got := r.Snapshot("room-1"); len(got) != 0 {
		t.Fatalf("after the last withdraw: %+v", got)
	}
}
