package extgov

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestNetGuardFailClosed(t *testing.T) {
	empty := NewNetGuard(nil)
	if empty.AllowHost("example.com") {
		t.Fatal("empty allowlist must deny all (fail closed)")
	}
	g := NewNetGuard([]string{"example.com", "api.internal:8443"})
	if !g.AllowHost("example.com") {
		t.Fatal("allowlisted host must be permitted")
	}
	if !g.AllowHost("example.com:443") {
		t.Fatal("allowlisted host must match any port")
	}
	if !g.AllowHost("api.internal:1234") {
		t.Fatal("allowlist entry with a port must still match any port on that host")
	}
	if g.AllowHost("evil.com") {
		t.Fatal("non-allowlisted host must be denied")
	}
}

func TestNetGuardAllowURL(t *testing.T) {
	g := NewNetGuard([]string{"good.com"})
	if !g.AllowURL("https://good.com/path") {
		t.Fatal("URL on allowlisted host must be permitted")
	}
	if g.AllowURL("https://bad.com/path") {
		t.Fatal("URL on non-allowlisted host must be denied")
	}
	if g.AllowURL("::not a url") {
		t.Fatal("unparseable URL must be denied")
	}
}

func TestNetGuardRoundTripperBlocks(t *testing.T) {
	g := NewNetGuard([]string{"good.com"})
	rt := g.RoundTripper(nil)
	req, _ := http.NewRequest(http.MethodGet, "https://bad.com/x", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip to a non-allowlisted host must fail closed before hitting the network")
	}
}

func TestCheckAddr_refusesMetadataAndLinkLocalButAllowsPrivate(t *testing.T) {
	for _, raw := range []string{"169.254.169.254", "fe80::1", "0.0.0.0", "224.0.0.1", "100.100.100.200", "fd00:ec2::254", "::ffff:169.254.169.254"} {
		if err := CheckAddr(netip.MustParseAddr(raw)); err == nil {
			t.Fatalf("CheckAddr(%s) = nil; want refusal", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "10.0.0.5", "172.18.0.3", "192.168.1.2", "8.8.8.8", "::1"} {
		if err := CheckAddr(netip.MustParseAddr(raw)); err != nil {
			t.Fatalf("CheckAddr(%s) = %v; want allowed", raw, err)
		}
	}
}

func TestNetGuardRoundTripper_refusesAllowlistedHostResolvingToLinkLocal(t *testing.T) {
	// The name is allowlisted, but the connection target is the metadata
	// address: the connect-time check refuses it before any packet is sent.
	client := &http.Client{Transport: NewNetGuard([]string{"169.254.169.254"}).RoundTripper(nil)}
	resp, err := client.Get("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("metadata address must be refused at connect time")
	}
	if !strings.Contains(err.Error(), "not reachable by extensions") {
		t.Fatalf("error = %v; want connect-time refusal", err)
	}
}
