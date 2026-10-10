// Package extgov holds the Layer-2 governance primitives for the extension
// foundation: what a deployment PERMITS an installed extension to do, enforced
// at runtime rather than by refusing to host the extension. It is kind-agnostic
// — the pipeline and each kind's Probe/Activate/drive sites take a Guard and
// consult it before reaching an external host.
package extgov

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// NetGuard enforces an extension's permissions.network allowlist. A nil or empty
// allowlist denies all outbound hosts (fail closed): an extension that declares
// no network permission cannot reach anything. Hosts are matched case-insensitively
// by hostname; an entry without a port matches any port on that host.
type NetGuard struct {
	allow map[string]bool // host (lowercased, no port) -> allowed
}

// NewNetGuard builds a guard from an allowlist of host entries (host or
// host:port; the port is ignored — allow the host, any port).
func NewNetGuard(allowlist []string) NetGuard {
	allow := make(map[string]bool, len(allowlist))
	for _, entry := range allowlist {
		h := hostOnly(strings.TrimSpace(strings.ToLower(entry)))
		if h != "" {
			allow[h] = true
		}
	}
	return NetGuard{allow: allow}
}

// AllowHost reports whether outbound traffic to host (host or host:port) is
// permitted. Fail closed: an empty allowlist permits nothing.
func (g NetGuard) AllowHost(host string) bool {
	return g.allow[hostOnly(strings.ToLower(strings.TrimSpace(host)))]
}

// AllowURL reports whether the URL's host is permitted.
func (g NetGuard) AllowURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return g.AllowHost(u.Host)
}

// RoundTripper wraps base so every request to a non-allowlisted host fails
// closed with an error, never reaching the network. Inject this into a kind's
// outbound HTTP client so permissions.network is enforced for every call the
// extension makes (each redirect hop is a new request and is checked too).
//
// base nil uses guardedTransport: http.DefaultTransport's settings plus a
// connect-time address check on the resolved IP (see checkDialAddr), so an
// allowlisted name that resolves (or is rebound) to a link-local or cloud
// metadata address is refused even though the name itself is allowed.
func (g NetGuard) RoundTripper(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = guardedTransport
	}
	return netGuardTransport{guard: g, base: base}
}

// guardedTransport is the shared transport of every extension client: one
// connection pool, the default transport's proxy/TLS/idle settings, and the
// connect-time address check.
var guardedTransport http.RoundTripper = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: checkDialAddr}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
	return t
}()

// metadataAddrs are cloud metadata endpoints outside the link-local ranges.
var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
	netip.MustParseAddr("fd00:ec2::254"),   // AWS IPv6
}

// checkDialAddr runs on the resolved address of every connection an
// extension client opens. Loopback and private addresses stay allowed
// (sidecars listen on loopback, endpoint components sit on the deployment's
// private network); addresses no extension may ever reach are refused:
// unspecified, multicast, link-local (incl. 169.254.169.254) and known cloud
// metadata addresses.
func checkDialAddr(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("extgov: unparseable address %q", host)
	}
	return CheckAddr(addr)
}

// CheckAddr reports whether an extension may connect to addr (the rule of
// checkDialAddr, exported for tests and diagnostics).
func CheckAddr(addr netip.Addr) error {
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() {
		return fmt.Errorf("extgov: address %s is not reachable by extensions", addr)
	}
	for _, m := range metadataAddrs {
		if addr == m {
			return fmt.Errorf("extgov: metadata address %s is not reachable by extensions", addr)
		}
	}
	return nil
}

type netGuardTransport struct {
	guard NetGuard
	base  http.RoundTripper
}

func (t netGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.guard.AllowHost(req.URL.Host) {
		return nil, fmt.Errorf("extgov: host %q not in the extension's permissions.network allowlist", req.URL.Host)
	}
	return t.base.RoundTrip(req)
}

func hostOnly(hostport string) string {
	if hostport == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// Guard is the per-extension governance context handed to a kind's Probe,
// Activate, and drive sites: the network allowlist every outbound call of the
// extension is held to. Tenant and rollout distribution are read-time filters
// on the install record (extpipe.Record.AllowsTenant / Rollout), not part of
// the driver's guard.
type Guard struct {
	Net NetGuard
}
