package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
)

const testCLINonce = "cli-nonce-0123456789abcdefghijklmnopqrstuv"

func claimSSO(t *testing.T, srvURL, nonce string) (int, sessionGrant) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"nonce": nonce})
	resp, err := http.Post(srvURL+"/v1/auth/sso/claim", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var g sessionGrant
	_ = json.Unmarshal(raw, &g)
	return resp.StatusCode, g
}

// A CLI-initiated flow parks the grant under the nonce: the browser gets only
// #cliSignedIn=1 (no token, no code), and the CLI claims the grant exactly
// once with its nonce — 404 before the sign-in completes and after the claim.
func TestCLISSOClaimFlow(t *testing.T) {
	s := ssoGateway(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	if c, _ := claimSSO(t, srv.URL, testCLINonce); c != http.StatusNotFound {
		t.Fatalf("claim before ACS: %d, want 404", c)
	}

	rec := completeSSOFor(t, s, "alice", "https://gw.example.com/sso-callback?cli_nonce="+url.QueryEscape(testCLINonce))
	if rec.Code != http.StatusFound {
		t.Fatalf("ACS status = %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	frag, _ := url.ParseQuery(loc.Fragment)
	if frag.Get("cliSignedIn") != "1" || len(frag) != 1 || strings.Contains(loc.RawQuery, "cli_nonce") || loc.Path != "/sso-callback" {
		t.Fatalf("CLI redirect = %s, want /sso-callback#cliSignedIn=1 with nothing else", loc)
	}

	if c, _ := claimSSO(t, srv.URL, "wrong-nonce-0123456789abcdefghijklmnopq"); c != http.StatusNotFound {
		t.Fatalf("wrong nonce: %d, want 404", c)
	}
	c, g := claimSSO(t, srv.URL, testCLINonce)
	if c != http.StatusOK || g.Subject != "alice" || g.RefreshToken == "" {
		t.Fatalf("claim = %d %+v, want alice's grant", c, g)
	}
	if claims, err := s.Signer.Verify(g.Token); err != nil || claims.Subject != "alice" {
		t.Fatalf("claimed token invalid: %v", err)
	}
	if c, _ := claimSSO(t, srv.URL, testCLINonce); c != http.StatusNotFound {
		t.Fatalf("second claim: %d, want 404 (single use)", c)
	}
}

// The grant expires if nobody claims it; it lives at least two minutes.
func TestCLISSOGrantExpires(t *testing.T) {
	if cliGrantTTL < 2*time.Minute {
		t.Fatalf("cliGrantTTL = %v, want >= 2m", cliGrantTTL)
	}
	s := ssoGateway(t)
	ctx := context.Background()
	now := time.Now()
	s.Now = func() time.Time { return now }
	if err := s.parkCLIHandoff(ctx, auth.Identity{Subject: "alice"}, testCLINonce); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return now.Add(cliGrantTTL + time.Second) }
	if _, ok, _ := s.claimCLIHandoff(ctx, testCLINonce); ok {
		t.Fatal("expired grant claimed")
	}
	s.Now = func() time.Time { return now }
	if err := s.parkCLIHandoff(ctx, auth.Identity{Subject: "alice"}, testCLINonce); err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return now.Add(cliGrantTTL - time.Second) }
	if id, ok, _ := s.claimCLIHandoff(ctx, testCLINonce); !ok || id.Subject != "alice" {
		t.Fatal("live grant not claimable")
	}
}

// A second sign-in for a nonce that is still pending cannot swap the grant
// the CLI is waiting for, and a short (guessable) nonce is refused.
func TestCLISSORefusesReplacedOrWeakNonce(t *testing.T) {
	s := ssoGateway(t)
	relay := "/sso-callback?cli_nonce=" + url.QueryEscape(testCLINonce)
	if rec := completeSSOFor(t, s, "alice", relay); rec.Code != http.StatusFound {
		t.Fatalf("first ACS: %d", rec.Code)
	}
	if rec := completeSSOFor(t, s, "mallory", relay); rec.Code != http.StatusConflict {
		t.Fatalf("second ACS for a pending nonce: %d, want 409", rec.Code)
	}
	if id, ok, _ := s.claimCLIHandoff(context.Background(), testCLINonce); !ok || id.Subject != "alice" {
		t.Fatalf("pending grant replaced: %+v", id)
	}
	if rec := completeSSOFor(t, s, "alice", "/sso-callback?cli_nonce=short"); rec.Code != http.StatusBadRequest {
		t.Fatalf("short nonce: %d, want 400", rec.Code)
	}
	if rec := completeSSOFor(t, s, "alice", relay+"&desktop_nonce=n"); rec.Code != http.StatusBadRequest {
		t.Fatalf("two hand-off flows: %d, want 400", rec.Code)
	}
}

// The relay cookie carries cli_nonce through an IdP that drops RelayState.
func TestCLISSOSurvivesIdPDroppingRelayState(t *testing.T) {
	s := ssoGateway(t)
	redirect := "https://gw.example.com/sso-callback?cli_nonce=" + url.QueryEscape(testCLINonce)
	start := httptest.NewRequest(http.MethodGet, "https://gw.example.com/api/auth/init?redirect_uri="+url.QueryEscape(redirect), nil)
	start.Header.Set("X-Forwarded-Proto", "https")
	startRec := httptest.NewRecorder()
	s.rememberSSORelay(startRec, start)
	cookies := startRec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("relay cookie not set: %+v", cookies)
	}
	acs := httptest.NewRequest(http.MethodPost, "https://gw.example.com/api/auth/acs", nil)
	acs.Header.Set("X-Forwarded-Proto", "https")
	acs.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	s.completeSSO(rec, acs, auth.Identity{Subject: "alice"}, "/")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	frag, _ := url.ParseQuery(loc.Fragment)
	if rec.Code != http.StatusFound || frag.Get("cliSignedIn") != "1" || frag.Get("sessionToken") != "" {
		t.Fatalf("redirect = %d %s, want the CLI hand-off", rec.Code, loc)
	}
	if id, ok, _ := s.claimCLIHandoff(context.Background(), testCLINonce); !ok || id.Subject != "alice" {
		t.Fatal("grant not claimable after relay-cookie flow")
	}
}

func TestCLISSOClaimRequiresNonce(t *testing.T) {
	srv := httptest.NewServer(ssoGateway(t).Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/auth/sso/claim", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("claim without nonce: %d, want 400", resp.StatusCode)
	}
}
