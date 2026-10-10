package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
)

// An IdP-hosted login page drops RelayState: the return address remembered at
// sign-in start (relay cookie) must still route a desktop-initiated flow to the
// nonce-bound one-time code, never a browser session.
func TestDesktopSSOSurvivesIdPDroppingRelayState(t *testing.T) {
	s := ssoGateway(t)
	start := httptest.NewRequest(http.MethodGet, "https://gw.example.com/api/auth/init?redirect_uri="+url.QueryEscape("https://gw.example.com/sso-callback?desktop_nonce=n-1"), nil)
	start.Header.Set("X-Forwarded-Proto", "https")
	startRec := httptest.NewRecorder()
	s.rememberSSORelay(startRec, start)
	cookies := startRec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != ssoRelayCookie || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteNoneMode {
		t.Fatalf("relay cookie = %+v, want one Secure HttpOnly SameSite=None cookie", cookies)
	}

	acs := httptest.NewRequest(http.MethodPost, "https://gw.example.com/api/sso/acs", nil)
	acs.Header.Set("X-Forwarded-Proto", "https")
	acs.AddCookie(cookies[0])
	rec := httptest.NewRecorder()
	// The provider's relay is its default (RelayState was dropped).
	s.completeSSO(rec, acs, auth.Identity{Subject: "alice"}, "/")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	frag, _ := url.ParseQuery(loc.Fragment)
	if rec.Code != http.StatusFound || frag.Get("ssoCode") == "" || frag.Get("sessionToken") != "" {
		t.Fatalf("redirect = %d %s, want the desktop ssoCode hand-off", rec.Code, loc)
	}
	// The relay is single-use: the response clears the cookie.
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == ssoRelayCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("relay cookie not cleared after use")
	}
}

// A cookie that is not a gateway-signed relay (e.g. a session token or junk)
// is ignored; the provider's relay is used.
func TestSSORelayCookieMustBeSignedRelay(t *testing.T) {
	s := ssoGateway(t)
	session := s.Signer.Issue(auth.Claims{Subject: "https://gw.example.com/sso-callback?desktop_nonce=x", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	for _, value := range []string{session, "junk"} {
		acs := httptest.NewRequest(http.MethodPost, "https://gw.example.com/api/auth/acs", nil)
		acs.AddCookie(&http.Cookie{Name: ssoRelayCookie, Value: value})
		rec := httptest.NewRecorder()
		s.completeSSO(rec, acs, auth.Identity{Subject: "alice"}, "/sso-callback")
		loc, _ := url.Parse(rec.Header().Get("Location"))
		frag, _ := url.ParseQuery(loc.Fragment)
		if frag.Get("ssoCode") != "" || frag.Get("sessionToken") == "" || loc.Path != "/sso-callback" {
			t.Fatalf("forged relay cookie %q changed the flow: %s", value[:4], loc)
		}
	}
}

// An invalid redirect_uri is never remembered.
func TestSSORelayRejectsForeignRedirect(t *testing.T) {
	s := ssoGateway(t)
	rec := httptest.NewRecorder()
	s.rememberSSORelay(rec, httptest.NewRequest(http.MethodGet, "https://gw.example.com/api/auth/init?redirect_uri="+url.QueryEscape("https://evil.example/cb"), nil))
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("foreign redirect_uri was remembered")
	}
}
