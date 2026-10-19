package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// A renewal that fails in transit keeps the turn: only the server's refusal
// (it took the job back) ends it.
func TestLeaseSurvivesTransientRenewalFailures(t *testing.T) {
	var mu sync.Mutex
	answers := []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusOK, http.StatusBadGateway, http.StatusOK, http.StatusConflict}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		code := http.StatusConflict
		if calls < len(answers) {
			code = answers[calls]
		}
		calls++
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "alice"})
	b.leaseRenewOverride = 10 * time.Millisecond
	lost := make(chan struct{})
	go b.renewLeaseLoop(context.Background(), "job-1", func() { close(lost) })
	select {
	case <-lost:
	case <-time.After(5 * time.Second):
		t.Fatal("a refused renewal did not end the turn")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != len(answers) {
		t.Fatalf("the turn ended after %d renewals, want %d (only at the refusal)", calls, len(answers))
	}
}
