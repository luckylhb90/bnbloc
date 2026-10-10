package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
)

// Invite redemption is authenticated, so its code-guessing throttle is keyed
// on the subject: one account is bounded, while other users behind the same
// proxy/NAT (same client IP) keep their own bucket.
func TestInviteRedeemThrottleIsPerSubject(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	redeem := func(subject string) int {
		t.Helper()
		token := gw.Signer.Issue(auth.Claims{Subject: subject, Role: "member", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/invites/code123/redeem", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("redeem as %s: %v", subject, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	for i := 0; i < int(defaultGuessBurst); i++ {
		if got := redeem("alice"); got != http.StatusOK {
			t.Fatalf("alice redeem #%d = %d, want 200", i+1, got)
		}
	}
	if got := redeem("alice"); got != http.StatusTooManyRequests {
		t.Fatalf("alice redeem past burst = %d, want 429", got)
	}
	// Same client IP (loopback), different subject: not throttled.
	if got := redeem("bob"); got != http.StatusOK {
		t.Fatalf("bob redeem from the same IP = %d, want 200 (per-subject bucket)", got)
	}
}

// The desktop SSO code exchange is not IP-throttled: the code is 256 random
// bits bound to a nonce, and a per-IP bucket would make everyone behind one
// proxy share it at sign-in.
func TestSSOExchangeIsNotIPThrottled(t *testing.T) {
	srv := httptest.NewServer(newGateway(t, "").Handler())
	defer srv.Close()
	for i := 0; i < int(defaultGuessBurst)*3; i++ {
		resp, err := http.Post(srv.URL+"/v1/auth/sso/exchange", "application/json",
			strings.NewReader(`{"code":"unknown","nonce":"n"}`))
		if err != nil {
			t.Fatalf("exchange #%d: %v", i+1, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("exchange #%d throttled; the one-time code needs no per-IP limit", i+1)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("exchange #%d = %d, want 401 for an unknown code", i+1, resp.StatusCode)
		}
	}
}
