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

// Entering emits join+snapshot; a leave is announced only after the grace window
// elapses (deferred), then emits presence_leave+snapshot.
func TestPresenceJoinThenDeferredLeave(t *testing.T) {
	var events []wire.Event
	p, timers := newTestPresence(func(ev wire.Event) { events = append(events, ev) })

	p.Enter("room-1", "alice")
	if len(events) != 2 || events[0].Kind != "presence_join" || events[1].Kind != "presence_snapshot" {
		t.Fatalf("enter events = %v, want join+snapshot", kinds(events))
	}

	events = nil
	p.Leave("room-1", "alice")
	// Leave is deferred: nothing emitted yet, and the member is still present.
	if len(events) != 0 {
		t.Fatalf("immediate leave events = %v, want none (deferred)", kinds(events))
	}
	if got := p.Members("room-1"); len(got) != 1 {
		t.Fatalf("members during grace = %v, want [alice] still present", got)
	}

	timers.fire() // grace window elapses with no reconnect
	if len(events) != 2 || events[0].Kind != "presence_leave" || events[1].Kind != "presence_snapshot" {
		t.Fatalf("finalized leave events = %v, want leave+snapshot", kinds(events))
	}
	if got := p.Members("room-1"); len(got) != 0 {
		t.Fatalf("members after leave = %v, want empty", got)
	}
}

// A reconnect within the grace window cancels the pending leave and does not
// re-announce a join — the roster never flickers.
func TestPresenceReconnectCancelsLeave(t *testing.T) {
	var joins, leaves int
	p, timers := newTestPresence(func(ev wire.Event) {
		switch ev.Kind {
		case "presence_join":
			joins++
		case "presence_leave":
			leaves++
		}
	})
	p.Enter("r", "alice") // joins=1
	p.Leave("r", "alice") // schedules deferred leave
	p.Enter("r", "alice") // reconnect within grace: cancels pending leave

	timers.fire() // any (cancelled) timer would fire here

	if joins != 1 {
		t.Fatalf("joins = %d, want 1 (reconnect must not re-announce join)", joins)
	}
	if leaves != 0 {
		t.Fatalf("leaves = %d, want 0 (reconnect must cancel the leave)", leaves)
	}
	if got := p.Members("r"); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("members = %v, want [alice] still present", got)
	}
}

// A second device does not emit a duplicate join; the leave is scheduled only
// when the last live reference goes.
func TestPresenceRefcountDefersOnLastLeave(t *testing.T) {
	var joins, leaves int
	p, timers := newTestPresence(func(ev wire.Event) {
		switch ev.Kind {
		case "presence_join":
			joins++
		case "presence_leave":
			leaves++
		}
	})
	p.Enter("r", "alice")
	p.Enter("r", "alice") // second device
	if joins != 1 {
		t.Fatalf("joins = %d, want 1", joins)
	}
	p.Leave("r", "alice") // one device gone; still present, no schedule
	timers.fire()
	if leaves != 0 {
		t.Fatalf("leaves after first device = %d, want 0", leaves)
	}
	p.Leave("r", "alice") // last device gone; schedules deferred leave
	timers.fire()
	if leaves != 1 {
		t.Fatalf("leaves after last device = %d, want 1", leaves)
	}
}

func kinds(events []wire.Event) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}
