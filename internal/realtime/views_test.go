package realtime

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// viewsReplica is one replica's views, its streams' events recorded.
type viewsReplica struct {
	v   *RoomViews
	mu  sync.Mutex
	got []wire.Event
}

func (r *viewsReplica) events() []wire.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.got
	r.got = nil
	return out
}

// viewsCluster wires n replicas' views over a synchronous bus (nil when
// n == 1: no bus).
func viewsCluster(n int) []*viewsReplica {
	reps := make([]*viewsReplica, n)
	for i := range reps {
		reps[i] = &viewsReplica{}
	}
	for i, r := range reps {
		var publish func(wire.Event)
		if n > 1 {
			publish = func(ev wire.Event) {
				for k, other := range reps {
					if k != i {
						other.v.Remote(ev)
					}
				}
			}
		}
		r.v = NewRoomViews(publish, func(ev wire.Event) { r.mu.Lock(); r.got = append(r.got, ev); r.mu.Unlock() })
	}
	return reps
}

func deltaOf(t *testing.T, evs []wire.Event) (joined, left []string) {
	t.Helper()
	for _, ev := range evs {
		if ev.Kind != wire.EventPresenceDelta {
			continue
		}
		p := ev.Payload.(wire.PresenceDeltaPayload)
		joined, left = append(joined, p.Joined...), append(left, p.Left...)
	}
	return joined, left
}

// With no bus the room's view is the replica's: its changes reach its
// streams as they are.
func TestRoomViewsAlone(t *testing.T) {
	r := viewsCluster(1)[0]
	r.v.LocalPresence("room", []string{"maya", "jack"})
	if j, l := deltaOf(t, r.events()); !slices.Equal(j, []string{"jack", "maya"}) || len(l) != 0 {
		t.Fatalf("joined %v left %v", j, l)
	}
	r.v.LocalPresence("room", []string{"maya"})
	if j, l := deltaOf(t, r.events()); len(j) != 0 || !slices.Equal(l, []string{"jack"}) {
		t.Fatalf("joined %v left %v", j, l)
	}
	if got := r.v.Members("room"); !slices.Equal(got, []string{"maya"}) {
		t.Fatalf("members %v", got)
	}
}

// A member on two replicas (two devices) is here until gone from both:
// one replica's departure tells no stream anything.
func TestRoomViewsJoinReplicas(t *testing.T) {
	reps := viewsCluster(2)
	a, b := reps[0], reps[1]
	a.v.LocalPresence("room", []string{"maya"})
	b.v.LocalPresence("room", []string{"maya", "jack"})
	if j, _ := deltaOf(t, a.events()); !slices.Equal(j, []string{"maya", "jack"}) {
		t.Fatalf("a's streams were told %v joined", j)
	}
	if j, _ := deltaOf(t, b.events()); !slices.Equal(j, []string{"maya", "jack"}) {
		t.Fatalf("b's streams were told %v joined", j)
	}
	a.v.LocalPresence("room", nil)
	for _, r := range reps {
		if j, l := deltaOf(t, r.events()); len(j)+len(l) != 0 {
			t.Fatalf("maya left one replica: told joined %v left %v", j, l)
		}
	}
	b.v.LocalPresence("room", []string{"jack"})
	for _, r := range reps {
		if _, l := deltaOf(t, r.events()); !slices.Equal(l, []string{"maya"}) {
			t.Fatalf("told %v left, want maya", l)
		}
		if got := r.v.Members("room"); !slices.Equal(got, []string{"jack"}) {
			t.Fatalf("members %v", got)
		}
	}
}

// A replica that starts receiving a room asks for the others' views; the
// roster is the union of the replicas' bridges.
func TestRoomViewsQueryAndRoster(t *testing.T) {
	reps := viewsCluster(2)
	a, b := reps[0], reps[1]
	pub := a.v.publish
	a.v.publish = nil // b is not on the bus yet
	a.v.LocalPresence("room", []string{"maya"})
	a.v.LocalRoster("room", []wire.RosterAgent{{OwnerUserID: "maya", Provider: "codex"}})
	a.v.publish = pub
	if len(b.v.Members("room")) != 0 {
		t.Fatal("b knew a's view before asking")
	}
	b.v.Query("room")
	if j, _ := deltaOf(t, b.events()); !slices.Equal(j, []string{"maya"}) {
		t.Fatalf("after the query b's streams were told %v joined", j)
	}
	b.v.LocalRoster("room", []wire.RosterAgent{{OwnerUserID: "jack", Provider: "claude"}})
	want := []wire.RosterAgent{{OwnerUserID: "jack", Provider: "claude"}, {OwnerUserID: "maya", Provider: "codex"}}
	for _, r := range reps {
		if got := r.v.Roster("room"); !slices.Equal(got, want) {
			t.Fatalf("roster %v, want %v", got, want)
		}
	}
	var last wire.Event
	for _, ev := range a.events() {
		if ev.Kind == wire.EventAgentRoster {
			last = ev
		}
	}
	if agents := last.Payload.(map[string]any)["agents"].([]wire.RosterAgent); !slices.Equal(agents, want) {
		t.Fatalf("a's streams were told %v", agents)
	}
}

// A replica that stopped announcing (it died) is forgotten after
// viewExpiry: its members leave.
func TestRoomViewsExpireASilentReplica(t *testing.T) {
	reps := viewsCluster(2)
	a, b := reps[0], reps[1]
	now := time.Now()
	b.v.now = func() time.Time { return now }
	a.v.LocalPresence("room", []string{"maya"})
	b.events()
	now = now.Add(viewExpiry + time.Second)
	b.v.refresh()
	if _, l := deltaOf(t, b.events()); !slices.Equal(l, []string{"maya"}) {
		t.Fatalf("b's streams were told %v left, want maya", l)
	}
}
