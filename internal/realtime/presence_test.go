package realtime

import (
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// controllableTimers replaces Presence.after with a manual trigger, so tests
// exercise the deferred-leave contract without waiting for the real grace timer.
type controllableTimers struct{ fns []func() }

func (c *controllableTimers) after(_ time.Duration, fn func()) *time.Timer {
	c.fns = append(c.fns, fn)
	// Return a real (stopped) timer so callers can Stop() it safely; the actual
	// callback is invoked by fire(), not by wall-clock time.
	t := time.NewTimer(time.Hour)
	t.Stop()
	return t
}

// fire runs all pending deferred callbacks (simulating the grace window elapsing).
func (c *controllableTimers) fire() {
	fns := c.fns
	c.fns = nil
	for _, fn := range fns {
		fn()
	}
}

func newTestPresence(publish func(wire.Event)) (*Presence, *controllableTimers) {
	timers := &controllableTimers{}
	p := NewPresence(publish)
	p.after = timers.after
	return p, timers
}

// deltas totals the arrivals and departures announced in events.
func deltas(events []wire.Event) (joined, left []string) {
	for _, ev := range events {
		if ev.Kind != wire.EventPresenceDelta {
			continue
		}
		d := ev.Payload.(wire.PresenceDeltaPayload)
		joined, left = append(joined, d.Joined...), append(left, d.Left...)
	}
	return joined, left
}

// An arrival is announced in the room's next delta (the snapshot is each
// stream's own, Snapshot); a departure only after the grace window, in the
// delta after it.
func TestPresenceJoinThenDeferredLeave(t *testing.T) {
	var events []wire.Event
	p, timers := newTestPresence(func(ev wire.Event) { events = append(events, ev) })

	p.Enter("room-1", "alice")
	if len(events) != 0 {
		t.Fatalf("an arrival announced before the delta: %v", kinds(events))
	}
	timers.fire() // the delta
	if j, l := deltas(events); len(j) != 1 || j[0] != "alice" || len(l) != 0 {
		t.Fatalf("delta = joined %v left %v, want alice joined", j, l)
	}
	if snap := p.Snapshot("room-1"); snap.Kind != "presence_snapshot" || len(snap.Payload.(map[string]any)["members"].([]string)) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}

	events = nil
	p.Leave("room-1", "alice")
	if len(events) != 0 {
		t.Fatalf("immediate leave events = %v, want none (deferred)", kinds(events))
	}
	if got := p.Members("room-1"); len(got) != 1 {
		t.Fatalf("members during grace = %v, want [alice] still present", got)
	}
	timers.fire() // grace window elapses with no reconnect
	timers.fire() // the delta
	if j, l := deltas(events); len(j) != 0 || len(l) != 1 || l[0] != "alice" {
		t.Fatalf("delta = joined %v left %v, want alice left", j, l)
	}
	if got := p.Members("room-1"); len(got) != 0 {
		t.Fatalf("members after leave = %v, want empty", got)
	}
}

// Many arrivals in one window go out as one delta.
func TestPresenceDeltaCoalesces(t *testing.T) {
	var events []wire.Event
	p, timers := newTestPresence(func(ev wire.Event) { events = append(events, ev) })
	for _, u := range []string{"a", "b", "c"} {
		p.Enter("r", u)
	}
	timers.fire()
	if j, _ := deltas(events); len(events) != 1 || len(j) != 3 {
		t.Fatalf("events = %v (joined %v), want one delta of three", kinds(events), j)
	}
}

// A reconnect within the grace window cancels the pending leave and does not
// re-announce an arrival — the roster never flickers.
func TestPresenceReconnectCancelsLeave(t *testing.T) {
	var events []wire.Event
	p, timers := newTestPresence(func(ev wire.Event) { events = append(events, ev) })
	p.Enter("r", "alice")
	timers.fire()         // the arrival's delta
	p.Leave("r", "alice") // schedules deferred leave
	p.Enter("r", "alice") // reconnect within grace: cancels pending leave
	timers.fire()         // any (cancelled) timer would fire here
	timers.fire()
	joined, left := deltas(events)
	if len(joined) != 1 {
		t.Fatalf("joined = %v, want alice once (reconnect must not re-announce)", joined)
	}
	if len(left) != 0 {
		t.Fatalf("left = %v, want none (reconnect must cancel the leave)", left)
	}
	if got := p.Members("r"); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("members = %v, want [alice] still present", got)
	}
}

// A second device does not announce a second arrival; the leave is scheduled
// only when the last live reference goes.
func TestPresenceRefcountDefersOnLastLeave(t *testing.T) {
	var events []wire.Event
	p, timers := newTestPresence(func(ev wire.Event) { events = append(events, ev) })
	p.Enter("r", "alice")
	p.Enter("r", "alice") // second device
	timers.fire()
	if j, _ := deltas(events); len(j) != 1 {
		t.Fatalf("joined = %v, want alice once", j)
	}
	p.Leave("r", "alice") // one device gone; still present, no schedule
	timers.fire()
	if _, l := deltas(events); len(l) != 0 {
		t.Fatalf("left after first device = %v, want none", l)
	}
	p.Leave("r", "alice") // last device gone; schedules deferred leave
	timers.fire()
	timers.fire()
	if _, l := deltas(events); len(l) != 1 {
		t.Fatalf("left after last device = %v, want alice", l)
	}
}

func kinds(events []wire.Event) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}
