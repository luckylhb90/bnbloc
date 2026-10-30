package gateway

import (
	"net/http"
	"testing"
)

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

// The gateway's limits read the live replica count (the Redis census): two
// live replicas halve the per-subject chat burst.
func TestGatewayChatLimitSharedAcrossReplicas(t *testing.T) {
	gw := &Service{Replicas: func() int { return 2 }}
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
