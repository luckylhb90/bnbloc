package realtimehttp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/pulse"
	"github.com/luckylhb90/bnbloc/internal/pulse/pulsetest"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

var testPulseKey = []byte("0123456789abcdef0123456789abcdef")

func TestPulsePresenceCarriesOnlyTheHashedMember(t *testing.T) {
	e, rec := pulsetest.New(t)
	s := &Service{Pulse: e, PulseKey: testPulseKey}
	ctx := tenant.NewContext(context.Background(), "t1")

	stop := s.pulsePresenceStart(ctx, "r1", "alice@example.com", "")
	stop()

	evs := rec.Wait(t, 2)
	for _, ev := range evs {
		raw := ev["id"].(string) + ev["kind"].(string) + strings.Join(attrStrings(ev), " ")
		if strings.Contains(raw, "alice") {
			t.Fatalf("a person's id reached a pulse event: %v", ev)
		}
		if ev["tenant"] != "t1" {
			t.Fatalf("tenant = %v", ev["tenant"])
		}
	}
	if evs[0]["kind"] != pulse.KindPresenceSeen || pulsetest.Attr(evs[0], "country") != nil {
		t.Fatalf("first event = %v", evs[0])
	}
	if evs[len(evs)-1]["kind"] != pulse.KindPresenceLeft {
		t.Fatalf("the stream's end must emit presence.left: %v", evs[len(evs)-1])
	}
}

func TestPulsePresenceRefreshesWhileOpen(t *testing.T) {
	old := pulsePresenceEvery
	pulsePresenceEvery = 5 * time.Millisecond
	t.Cleanup(func() { pulsePresenceEvery = old })

	e, rec := pulsetest.New(t)
	s := &Service{Pulse: e, PulseKey: testPulseKey}
	stop := s.pulsePresenceStart(tenant.NewContext(context.Background(), "t1"), "r1", "alice", "")
	rec.Wait(t, 3) // the first seen, then refreshes while the stream stays open
	stop()
}

// Without a key no member is hashed, so nothing is emitted: a raw id is never
// the fallback.
func TestPulsePresenceWithoutKeyEmitsNothing(t *testing.T) {
	e, rec := pulsetest.New(t)
	s := &Service{Pulse: e}
	s.pulsePresenceStart(tenant.NewContext(context.Background(), "t1"), "r1", "alice", "")()
	time.Sleep(20 * time.Millisecond)
	if n := len(rec.Events()); n != 0 {
		t.Fatalf("%d events with no key", n)
	}
}

func TestPulseMessageSentChannelType(t *testing.T) {
	e, rec := pulsetest.New(t)
	s := &Service{Pulse: e}
	ctx := tenant.NewContext(context.Background(), "t1")
	s.pulseMessageSent(ctx, "r1", 41, false, "alice")
	s.pulseMessageSent(ctx, "r1", 42, true, "alice")
	evs := rec.Wait(t, 2)
	if evs[0]["id"] != "msg:r1:41" || pulsetest.Attr(evs[0], "channelType") != "room" {
		t.Fatalf("room message = %v", evs[0])
	}
	if pulsetest.Attr(evs[1], "channelType") != "agent" {
		t.Fatalf("agent message = %v", evs[1])
	}
}

func TestPulseHooksAreNoopWithoutAnEmitter(t *testing.T) {
	s := &Service{}
	s.pulseMessageSent(context.Background(), "r1", 1, true, "alice")
	s.pulsePresenceStart(context.Background(), "r1", "alice", "")()
}

func attrStrings(ev map[string]any) []string {
	var out []string
	attrs, _ := ev["attrs"].(map[string]any)
	for k, v := range attrs {
		if s, ok := v.(string); ok {
			out = append(out, k, s)
		}
	}
	return out
}

// Member locations: the country reaches the stream only when the organization
// has them on and the zone is one the table knows. The zone itself never does.
func TestPulsePresenceCountryNeedsTheOrganizationSwitch(t *testing.T) {
	e, rec := pulsetest.New(t)
	on := false
	gate := pulse.NewGeoGate(func(context.Context, string) (bool, error) { return on, nil }, 0)
	s := &Service{Pulse: e, PulseKey: testPulseKey, PulseGeo: gate}
	ctx := tenant.NewContext(context.Background(), "t1")

	s.pulsePresenceStart(ctx, "r1", "alice", "Europe/Berlin")()
	on = true
	s.pulsePresenceStart(ctx, "r1", "bob", "Europe/Berlin")()
	s.pulsePresenceStart(ctx, "r1", "carol", "Mars/Olympus")()

	evs := rec.Wait(t, 6)
	country := func(i int) any { return pulsetest.Attr(evs[i], "country") }
	if country(0) != nil || country(1) != nil {
		t.Fatalf("switch off: no country may be sent, got %v and %v", country(0), country(1))
	}
	if country(2) != "de" || country(3) != "de" {
		t.Fatalf("switch on: bob's seen and left carry country de, got %v and %v", country(2), country(3))
	}
	if country(4) != nil || country(5) != nil {
		t.Fatalf("an unknown zone must send no country, got %v and %v", country(4), country(5))
	}
	for _, ev := range evs {
		for _, v := range ev["attrs"].(map[string]any) {
			if s, ok := v.(string); ok && s == "Europe/Berlin" {
				t.Fatal("the time zone itself must never reach an event")
			}
		}
	}
}
