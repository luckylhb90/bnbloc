package gateway

import (
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// publicOrigin is the one answer to "where do clients reach this gateway":
// the configured public origin wins; without one the request's host, with
// a forwarded scheme believed only from a trusted proxy.
func TestPublicOrigin(t *testing.T) {
	r := httptest.NewRequest("GET", "http://gw.internal/v1/x", nil)
	r.RemoteAddr = "203.0.113.9:4000"
	r.Header.Set("X-Forwarded-Proto", "https")

	s := &Service{}
	if got := s.publicOrigin(r); got != "http://gw.internal" {
		t.Fatalf("untrusted peer's forwarded scheme believed: %q", got)
	}
	s.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}
	if got := s.publicOrigin(r); got != "https://gw.internal" {
		t.Fatalf("trusted proxy's scheme = %q", got)
	}
	s.PublicURL = "https://bnbloc.example.com/"
	r.Host = "evil.example.com"
	if got := s.publicOrigin(r); got != "https://bnbloc.example.com" {
		t.Fatalf("a caller's Host header leaked into a public link: %q", got)
	}
	if s.publicScheme(r) != "https" {
		t.Fatal("the configured origin's scheme decides")
	}
}

// TestForwardedHeadersReadOnce: X-Forwarded-Proto and X-Forwarded-For are
// client-writable, so only publicScheme and clientIP read them (each
// believing a trusted proxy only).
func TestForwardedHeadersReadOnce(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "service.go" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"X-Forwarded-Proto"`) || strings.Contains(string(raw), `"X-Forwarded-For"`) || strings.Contains(string(raw), `"X-Forwarded-Host"`) {
			t.Errorf("%s reads a forwarded header; use s.publicOrigin / s.publicScheme / s.clientIP", f)
		}
	}
}
