package gateway

import (
	"net/http"
	"testing"
	"time"
)

// The limiter allows up to burst immediately, then refuses until tokens refill
// at the configured rate.
func TestRateLimiterBurstThenRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := newRateLimiter(2, 3) // 2/sec, burst 3
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
	l := newRateLimiter(1, 1)
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
