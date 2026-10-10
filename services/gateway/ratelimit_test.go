package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/shard"
)

// The limiter allows up to burst immediately, then refuses until tokens refill
// at the configured rate.
func TestRateLimiterBurstThenRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := newRateLimiter(2, 3, nil) // 2/sec, burst 3
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.allow("alice") {
			t.Fatalf("burst token %d denied, want allowed", i)
		}
	}
	if l.allow("alice") {
		t.Fatal("4th call within the same instant allowed, want denied")
	}
	// After 1s, ~2 tokens refill.
	now = now.Add(time.Second)
	if !l.allow("alice") || !l.allow("alice") {
		t.Fatal("expected two refilled tokens after 1s")
	}
	if l.allow("alice") {
		t.Fatal("third call after 1s allowed, want denied (only ~2 refilled)")
	}
}

// Buckets are per key, so one subject exhausting its tokens does not affect
// another.
func TestRateLimiterPerKey(t *testing.T) {
	now := time.Unix(0, 0)
	l := newRateLimiter(1, 1, nil)
	l.now = func() time.Time { return now }
	if !l.allow("alice") || l.allow("alice") {
		t.Fatal("alice: want first allowed, second denied")
	}
	if !l.allow("bob") {
		t.Fatal("bob should have a full independent bucket")
	}
}

// Only cost-bearing writes (POST chat, borrow authorization) are limited; reads,
// SSE, and history paging are exempt.
func TestRateLimitedTargets(t *testing.T) {
	cases := []struct {
		method, path string
		limited      bool
	}{
		{http.MethodPost, "/v1/rooms/r/chat", true},
		{http.MethodPut, "/v1/rooms/r/shared-agents/shared-agent:codex/authorization", true},
		{http.MethodPost, "/v1/rooms/r/shared-agents/shared-agent:codex/authorization/record", true},
		{http.MethodGet, "/v1/rooms/r/chat", false},
		{http.MethodGet, "/v1/rooms/r/events", false},
		{http.MethodGet, "/v1/rooms/r/history", false},
		{http.MethodGet, "/v1/rooms/r/shared-agents-usage", false},
		{http.MethodPost, "/v1/rooms/r/join", false},
	}
	for _, c := range cases {
		if got := rateLimited(c.method, mustParseAPIPath(t, c.method, c.path)); got != c.limited {
			t.Fatalf("rateLimited(%s %s) = %v, want %v", c.method, c.path, got, c.limited)
		}
	}
}

// With N live gateway replicas each enforces 1/N of the configured rate and
// burst, so a subject spreading requests over every replica gets the
// configured budget in total, not N times it. The share follows the live
// count: a replica joining shrinks the bucket at once.
func TestRateLimiterDividesByLiveReplicas(t *testing.T) {
	now := time.Unix(0, 0)
	replicas := 3
	l := newRateLimiter(3, 9, func() int { return replicas }) // 3/s, burst 9 deployment-wide
	l.now = func() time.Time { return now }
	allowed := 0
	for l.allow("maya") {
		allowed++
	}
	if allowed != 3 {
		t.Fatalf("burst on one of 3 replicas = %d, want 3 (9/3)", allowed)
	}
	now = now.Add(time.Second) // refills 1 token (3/s / 3)
	if !l.allow("maya") || l.allow("maya") {
		t.Fatal("refill on one of 3 replicas is not 1 token/s")
	}
	// Back to one replica: the full budget applies again.
	replicas = 1
	now = now.Add(10 * time.Second)
	allowed = 0
	for l.allow("maya") {
		allowed++
	}
	if allowed != 9 {
		t.Fatalf("burst on a single replica = %d, want 9", allowed)
	}
	// The burst never falls below one token (a budget smaller than the
	// replica count still lets each replica pass a request).
	replicas = 50
	now = now.Add(time.Minute)
	if !l.allow("jack") {
		t.Fatal("a tiny per-replica share refused every request")
	}
}

// The gateway's limits read the live replica count from its peer set
// (BNBLOC_GATEWAY_PEERS): two live replicas halve the per-subject chat burst.
func TestGatewayChatLimitSharedAcrossReplicas(t *testing.T) {
	peers, err := shard.New(shard.Options{
		Peers: "10.0.0.1:8080,10.0.0.2:8080",
		Probe: func(context.Context, string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	peers.Refresh(context.Background())
	gw := &Service{Peers: peers}
	allowed := 0
	for i := 0; i < 50 && gw.rateAllow("maya"); i++ {
		allowed++
	}
	if want := int(defaultChatBurst / 2); allowed != want {
		t.Fatalf("chat burst with 2 replicas = %d, want %d", allowed, want)
	}
	single := &Service{}
	allowed = 0
	for i := 0; i < 50 && single.rateAllow("maya"); i++ {
		allowed++
	}
	if allowed != int(defaultChatBurst) {
		t.Fatalf("chat burst on a single gateway = %d, want %d", allowed, int(defaultChatBurst))
	}
}
