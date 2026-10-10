package realtimehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// End-to-end remote-roster path: a member's bridge advertises the agents it
// serves via the ?agents= query on its live SSE subscription, and ANOTHER
// member reads them via GET /v1/rooms/{id}/agents — without the gateway ever
// reaching the advertising member's machine. This is the mechanism that makes
// the composer picker work against a remote gateway. When the bridge
// disconnects, the agent is withdrawn after the grace window.
func TestRosterAdvertisedOverSSEVisibleToOtherMember(t *testing.T) {
	hub := realtime.NewHub(64, 16)
	mem := membership.NewStatic()
	mem.Add("room-1", "alice") // the advertising bridge owner
	mem.Add("room-1", "bob")   // another member reading the roster
	svc := &Service{Hub: hub, Membership: mem}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	// Alice's bridge subscribes with presence=0 and advertises acme/codex.
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/v1/rooms/room-1/events?presence=0&agents=acme%2Fcodex", nil)
	req.Header.Set("X-Bnbloc-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bridge subscribe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Wait until the advertisement is registered.
	if !waitFor(func() bool { return len(svc.Roster.Snapshot("room-1")) == 1 }) {
		t.Fatal("timed out waiting for roster advertisement")
	}

	// Bob reads the roster over the public read endpoint.
	agents := getRoster(t, srv.URL, "bob")
	if len(agents) != 1 || agents[0].OwnerUserID != "alice" ||
		agents[0].Core != "acme" || agents[0].Provider != "codex" {
		t.Fatalf("bob roster = %+v, want one alice acme/codex", agents)
	}

	// Bridge disconnects: the deferred withdraw finalizes after the ~5s grace
	// window (real timer in the running service), then the agent is gone.
	cancel()
	if !waitForUntil(func() bool { return len(svc.Roster.Snapshot("room-1")) == 0 }, 8*time.Second) {
		t.Fatal("timed out waiting for roster withdraw after disconnect")
	}
	if got := getRoster(t, srv.URL, "bob"); len(got) != 0 {
		t.Fatalf("roster after disconnect = %+v, want empty", got)
	}
}

// A non-member cannot read the room roster (fail closed).
func TestRosterRejectsNonMember(t *testing.T) {
	srv := httptest.NewServer((&Service{Hub: realtime.NewHub(64, 16), Membership: membership.NewStatic()}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/agents", nil)
	req.Header.Set("X-Bnbloc-User", "intruder")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("read roster: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func getRoster(t *testing.T, base, user string) []wire.RosterAgent {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/rooms/room-1/agents", nil)
	req.Header.Set("X-Bnbloc-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("read roster: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read roster status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Agents []wire.RosterAgent `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode roster: %v", err)
	}
	return out.Agents
}

func waitFor(cond func() bool) bool {
	return waitForUntil(cond, 2*time.Second)
}

func waitForUntil(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
